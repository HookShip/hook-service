// Package transport provides an SSRF-safe outbound HTTP client for webhook
// delivery. It resolves DNS and pins the connection to a validated address,
// rejects private, loopback, link-local, and cloud-metadata destinations by
// default, refuses redirects, verifies TLS, bounds every timeout, and discards
// the response body under a strict byte cap. It never logs payloads or headers.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Result is the payload-free outcome of a delivery attempt.
type Result struct {
	StatusCode    int
	DurationMS    int
	RetryAfter    time.Duration
	HasRetryAfter bool
}

// ErrBlockedAddress reports that a destination resolved to a disallowed address.
var ErrBlockedAddress = errors.New("transport: destination address is not allowed")

// Resolver resolves a host to candidate IP addresses. Tests inject a fake.
type Resolver interface {
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
}

type netResolver struct{ r *net.Resolver }

func (n netResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	addrs, err := n.r.LookupIP(ctx, network, host)
	return addrs, err
}

// Options configures the SSRF-safe client. Zero values select safe defaults.
type Options struct {
	ConnectTimeout        time.Duration
	ResponseHeaderTimeout time.Duration
	TotalTimeout          time.Duration
	MaxResponseBytes      int64
	// AllowLoopback permits loopback destinations. It is enabled only for the
	// evaluation memory profile delivering to a local sink; never in production.
	AllowLoopback bool
	// AllowPrivate permits RFC 1918 / ULA destinations. Intended for tests and
	// closed lab networks only.
	AllowPrivate bool
	// AllowPlaintext permits http:// destinations. Only meaningful together with
	// AllowLoopback for the evaluation profile.
	AllowPlaintext bool
	Resolver       Resolver
	UserAgent      string
}

func (o Options) withDefaults() Options {
	if o.ConnectTimeout <= 0 {
		o.ConnectTimeout = 5 * time.Second
	}
	if o.ResponseHeaderTimeout <= 0 {
		o.ResponseHeaderTimeout = 10 * time.Second
	}
	if o.TotalTimeout <= 0 {
		o.TotalTimeout = 30 * time.Second
	}
	if o.MaxResponseBytes <= 0 {
		o.MaxResponseBytes = 64 << 10
	}
	if o.Resolver == nil {
		o.Resolver = netResolver{r: net.DefaultResolver}
	}
	if o.UserAgent == "" {
		o.UserAgent = "hookship-delivery/1"
	}
	return o
}

// Client is a reusable SSRF-safe HTTP client.
type Client struct {
	options    Options
	httpClient *http.Client
}

// NewClient builds an SSRF-safe client.
func NewClient(options Options) *Client {
	options = options.withDefaults()
	client := &Client{options: options}
	baseDialer := &net.Dialer{Timeout: options.ConnectTimeout}

	httpTransport := &http.Transport{
		Proxy:                  nil, // never honor ambient proxy configuration
		ForceAttemptHTTP2:      true,
		MaxIdleConns:           32,
		MaxIdleConnsPerHost:    4,
		IdleConnTimeout:        30 * time.Second,
		TLSHandshakeTimeout:    options.ConnectTimeout,
		ResponseHeaderTimeout:  options.ResponseHeaderTimeout,
		ExpectContinueTimeout:  1 * time.Second,
		MaxResponseHeaderBytes: 32 << 10,
		DisableCompression:     true,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: false,
		},
		DialContext: func(ctx context.Context, network, address string) (net.Conn, error) {
			return client.safeDial(ctx, baseDialer, network, address)
		},
	}

	client.httpClient = &http.Client{
		Transport: httpTransport,
		Timeout:   options.TotalTimeout,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return errors.New("transport: redirects are refused for signed delivery")
		},
	}
	return client
}

// safeDial resolves the host, selects a validated address, and dials that exact
// pinned address so the connection cannot be re-pointed after validation (DNS
// rebinding defense).
func (c *Client) safeDial(ctx context.Context, dialer *net.Dialer, network, address string) (net.Conn, error) {
	host, port, err := net.SplitHostPort(address)
	if err != nil {
		return nil, fmt.Errorf("transport: invalid dial address: %w", err)
	}

	if literal := net.ParseIP(host); literal != nil {
		if !c.addressAllowed(literal) {
			return nil, fmt.Errorf("%w: %s", ErrBlockedAddress, literal)
		}
		return dialer.DialContext(ctx, network, net.JoinHostPort(literal.String(), port))
	}

	ips, err := c.options.Resolver.LookupIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("transport: resolve %q: %w", host, err)
	}
	for _, ip := range ips {
		if c.addressAllowed(ip) {
			return dialer.DialContext(ctx, network, net.JoinHostPort(ip.String(), port))
		}
	}
	return nil, fmt.Errorf("%w: %s resolved only to disallowed addresses", ErrBlockedAddress, host)
}

// addressAllowed enforces the SSRF policy for a resolved IP.
func (c *Client) addressAllowed(ip net.IP) bool {
	if ip == nil || ip.IsUnspecified() || ip.IsMulticast() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() {
		return false
	}
	if ip.IsLoopback() {
		return c.options.AllowLoopback
	}
	if isCloudMetadata(ip) {
		return false
	}
	if isPrivate(ip) {
		return c.options.AllowPrivate
	}
	return true
}

func isCloudMetadata(ip net.IP) bool {
	// The well-known IMDS addresses across major providers.
	for _, blocked := range []string{"169.254.169.254", "fd00:ec2::254", "100.100.100.200"} {
		if ip.Equal(net.ParseIP(blocked)) {
			return true
		}
	}
	return false
}

func isPrivate(ip net.IP) bool {
	if ip4 := ip.To4(); ip4 != nil {
		switch {
		case ip4[0] == 10:
			return true
		case ip4[0] == 172 && ip4[1] >= 16 && ip4[1] <= 31:
			return true
		case ip4[0] == 192 && ip4[1] == 168:
			return true
		case ip4[0] == 100 && ip4[1] >= 64 && ip4[1] <= 127: // carrier-grade NAT
			return true
		}
		return false
	}
	// Unique local addresses fc00::/7.
	return len(ip) == net.IPv6len && (ip[0]&0xfe) == 0xfc
}

// ValidateURL rejects an endpoint URL before any I/O: only http(s), no
// userinfo, a host present, and plaintext only where explicitly permitted.
func (c *Client) ValidateURL(raw string) error {
	parsed, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("transport: invalid URL: %w", err)
	}
	switch parsed.Scheme {
	case "https":
	case "http":
		if !c.options.AllowPlaintext {
			return errors.New("transport: plaintext http is not permitted for this profile")
		}
	default:
		return fmt.Errorf("transport: unsupported URL scheme %q", parsed.Scheme)
	}
	if parsed.User != nil {
		return errors.New("transport: URL userinfo is not permitted")
	}
	if parsed.Hostname() == "" {
		return errors.New("transport: URL host is required")
	}
	return nil
}

// Deliver posts the exact payload bytes with the supplied headers and returns a
// payload-free result. The response body is drained and discarded under the
// configured byte cap so a hostile endpoint cannot exhaust memory. The payload
// and headers are never logged by this package.
func (c *Client) Deliver(ctx context.Context, endpointURL, contentType string, headers map[string]string, payload []byte) (Result, error) {
	if err := c.ValidateURL(endpointURL); err != nil {
		return Result{}, err
	}
	started := time.Now()
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpointURL, strings.NewReader(string(payload)))
	if err != nil {
		return Result{}, fmt.Errorf("transport: build request: %w", err)
	}
	request.ContentLength = int64(len(payload))
	if contentType != "" {
		request.Header.Set("Content-Type", contentType)
	}
	request.Header.Set("User-Agent", c.options.UserAgent)
	request.Header.Set("Accept", "*/*")
	for name, value := range headers {
		request.Header.Set(name, value)
	}

	response, err := c.httpClient.Do(request)
	if err != nil {
		return Result{DurationMS: durationMS(started)}, err
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, c.options.MaxResponseBytes))
		_ = response.Body.Close()
	}()

	result := Result{
		StatusCode: response.StatusCode,
		DurationMS: durationMS(started),
	}
	if delay, ok := parseRetryAfter(response.Header.Get("Retry-After"), started); ok {
		result.RetryAfter = delay
		result.HasRetryAfter = true
	}
	return result, nil
}

func durationMS(started time.Time) int {
	elapsed := time.Since(started).Milliseconds()
	if elapsed < 0 {
		return 0
	}
	if elapsed > 3_600_000 {
		return 3_600_000
	}
	return int(elapsed)
}

// parseRetryAfter parses delta-seconds or an HTTP-date Retry-After value.
func parseRetryAfter(value string, reference time.Time) (time.Duration, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, false
	}
	if seconds, err := parseNonNegativeInt(value); err == nil {
		return time.Duration(seconds) * time.Second, true
	}
	if when, err := http.ParseTime(value); err == nil {
		delta := when.Sub(reference)
		if delta < 0 {
			delta = 0
		}
		return delta, true
	}
	return 0, false
}

func parseNonNegativeInt(value string) (int64, error) {
	if value == "" {
		return 0, errors.New("empty")
	}
	var result int64
	for _, char := range value {
		if char < '0' || char > '9' {
			return 0, errors.New("not an integer")
		}
		result = result*10 + int64(char-'0')
		if result > 86_400 {
			return 86_400, nil
		}
	}
	return result, nil
}
