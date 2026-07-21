// Package objectstore implements the S3-compatible ciphertext PayloadStore used
// by the object-store payload backend. It speaks the S3 REST API directly over
// net/http with AWS Signature V4 (internal/awsv4), so it works against Amazon
// S3, MinIO, and other S3-compatible stores without any third-party SDK on the
// delivery data plane.
//
// The store persists only opaque AES-256-GCM ciphertext under deterministic,
// payload-free object keys minted by the durability layer. Objects are
// immutable: PutImmutable never overwrites an existing key with different bytes,
// and every read is checksum-verified end to end before the bytes are returned.
package objectstore

import (
	"bytes"
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/runtime"
)

// checksumMetadataHeader stores the hex SHA-256 of the ciphertext as S3 user
// metadata so a HEAD can verify immutability without downloading the object.
const checksumMetadataHeader = "X-Amz-Meta-Sha256"

// maxObjectBytes bounds a single ciphertext object read to protect worker memory
// against a corrupt or hostile store. It comfortably exceeds the durable
// profiles' MaxPayloadBytes envelope plus GCM overhead.
const maxObjectBytes = 64 << 20

// Config is the validated object-store connection configuration. It is built by
// the config package, which fails closed on incomplete or unsafe production
// settings before a Client is constructed.
type Config struct {
	Endpoint    string
	Region      string
	Bucket      string
	PathStyle   bool
	UseTLS      bool
	CABundlePEM []byte
	Credentials awsv4.Credentials
	Timeout     time.Duration
	Clock       runtime.Clock

	// transportRT is a test seam. When nil a real HTTP transport is built.
	transportRT http.RoundTripper
}

// Client is an S3-compatible PayloadStore.
type Client struct {
	cfg     Config
	base    *url.URL
	http    *http.Client
	clock   runtime.Clock
	service string
}

var _ runtime.PayloadStore = (*Client)(nil)

// New validates cfg and constructs an S3-compatible object store client.
func New(cfg Config) (*Client, error) {
	if strings.TrimSpace(cfg.Endpoint) == "" {
		return nil, errors.New("objectstore: endpoint is required")
	}
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("objectstore: region is required")
	}
	if strings.TrimSpace(cfg.Bucket) == "" {
		return nil, errors.New("objectstore: bucket is required")
	}
	if !cfg.Credentials.Configured() {
		return nil, errors.New("objectstore: credentials are required")
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Clock == nil {
		cfg.Clock = runtime.SystemClock{}
	}

	base, err := normalizeEndpoint(cfg.Endpoint, cfg.UseTLS)
	if err != nil {
		return nil, err
	}

	transport := cfg.transportRT
	if transport == nil {
		httpTransport := &http.Transport{
			MaxIdleConns:        64,
			MaxIdleConnsPerHost: 16,
			IdleConnTimeout:     90 * time.Second,
		}
		if cfg.UseTLS {
			tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}
			if len(cfg.CABundlePEM) > 0 {
				pool := x509.NewCertPool()
				if !pool.AppendCertsFromPEM(cfg.CABundlePEM) {
					return nil, errors.New("objectstore: CA bundle contained no valid certificates")
				}
				tlsConfig.RootCAs = pool
			}
			httpTransport.TLSClientConfig = tlsConfig
		}
		transport = httpTransport
	}

	return &Client{
		cfg:     cfg,
		base:    base,
		http:    &http.Client{Timeout: cfg.Timeout, Transport: transport},
		clock:   cfg.Clock,
		service: "s3",
	}, nil
}

// Name identifies the store for status and metrics.
func (c *Client) Name() string { return "s3:" + c.cfg.Bucket }

// PutImmutable writes ciphertext to key. It is idempotent for identical bytes
// and never overwrites an existing key that holds different bytes.
func (c *Client) PutImmutable(ctx context.Context, key string, data []byte, checksum runtime.Hash) (runtime.ObjectPutResult, error) {
	if len(data) == 0 {
		return runtime.ObjectPutResult{}, errors.New("objectstore: ciphertext must not be empty")
	}
	if runtime.SHA256(data) != checksum {
		return runtime.ObjectPutResult{}, errors.New("objectstore: ciphertext does not match declared checksum")
	}

	existing, found, err := c.head(ctx, key)
	if err != nil {
		return runtime.ObjectPutResult{}, err
	}
	if found {
		if existing.checksum != checksum.String() {
			return runtime.ObjectPutResult{}, runtime.ErrPayloadObjectConflict
		}
		return runtime.ObjectPutResult{Version: existing.version(), AlreadyExisted: true}, nil
	}

	result, err := c.put(ctx, key, data, checksum)
	if errors.Is(err, errPreconditionFailed) {
		// A concurrent writer created the object first; verify it holds identical
		// bytes so immutability is preserved, then treat this as an idempotent no-op.
		existing, found, headErr := c.head(ctx, key)
		if headErr != nil {
			return runtime.ObjectPutResult{}, headErr
		}
		if !found || existing.checksum != checksum.String() {
			return runtime.ObjectPutResult{}, runtime.ErrPayloadObjectConflict
		}
		return runtime.ObjectPutResult{Version: existing.version(), AlreadyExisted: true}, nil
	}
	return result, err
}

// Get reads and verifies the ciphertext at key.
func (c *Client) Get(ctx context.Context, key string, checksum runtime.Hash) ([]byte, error) {
	req, err := c.newRequest(ctx, http.MethodGet, key, nil)
	if err != nil {
		return nil, err
	}
	if err := c.sign(req, awsv4.EmptyPayloadHash); err != nil {
		return nil, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("objectstore: get %s: %w", key, err)
	}
	defer drainClose(resp)

	if resp.StatusCode == http.StatusNotFound {
		return nil, runtime.ErrPayloadObjectNotFound
	}
	if resp.StatusCode != http.StatusOK {
		return nil, c.statusError("get", key, resp)
	}
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxObjectBytes+1))
	if err != nil {
		return nil, fmt.Errorf("objectstore: read %s: %w", key, err)
	}
	if len(data) > maxObjectBytes {
		return nil, fmt.Errorf("objectstore: object %s exceeds %d bytes", key, maxObjectBytes)
	}
	if runtime.SHA256(data) != checksum {
		return nil, fmt.Errorf("objectstore: object %s failed checksum verification", key)
	}
	return data, nil
}

// Delete removes the object at key. Missing objects are treated as deleted.
func (c *Client) Delete(ctx context.Context, key string) error {
	req, err := c.newRequest(ctx, http.MethodDelete, key, nil)
	if err != nil {
		return err
	}
	if err := c.sign(req, awsv4.EmptyPayloadHash); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore: delete %s: %w", key, err)
	}
	defer drainClose(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusNoContent, http.StatusNotFound:
		return nil
	default:
		return c.statusError("delete", key, resp)
	}
}

// Ping verifies the bucket is reachable.
func (c *Client) Ping(ctx context.Context) error {
	// A bucket-scoped list with max-keys=0 confirms reachability and access
	// without depending on any object existing.
	target := *c.bucketURL()
	query := target.Query()
	query.Set("max-keys", "0")
	query.Set("list-type", "2")
	target.RawQuery = query.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return err
	}
	req.URL = &target
	req.Host = target.Host
	if err := c.sign(req, awsv4.EmptyPayloadHash); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("objectstore: ping: %w", err)
	}
	defer drainClose(resp)
	if resp.StatusCode != http.StatusOK {
		return c.statusError("ping", c.cfg.Bucket, resp)
	}
	return nil
}

type objectMetadata struct {
	checksum  string
	etag      string
	versionID string
}

func (m objectMetadata) version() string {
	if m.versionID != "" {
		return m.versionID
	}
	return strings.Trim(m.etag, `"`)
}

func (c *Client) head(ctx context.Context, key string) (objectMetadata, bool, error) {
	req, err := c.newRequest(ctx, http.MethodHead, key, nil)
	if err != nil {
		return objectMetadata{}, false, err
	}
	if err := c.sign(req, awsv4.EmptyPayloadHash); err != nil {
		return objectMetadata{}, false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return objectMetadata{}, false, fmt.Errorf("objectstore: head %s: %w", key, err)
	}
	defer drainClose(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		return objectMetadata{
			checksum:  strings.ToLower(resp.Header.Get(checksumMetadataHeader)),
			etag:      resp.Header.Get("ETag"),
			versionID: resp.Header.Get("X-Amz-Version-Id"),
		}, true, nil
	case http.StatusNotFound:
		return objectMetadata{}, false, nil
	default:
		return objectMetadata{}, false, c.statusError("head", key, resp)
	}
}

var errPreconditionFailed = errors.New("objectstore: precondition failed")

func (c *Client) put(ctx context.Context, key string, data []byte, checksum runtime.Hash) (runtime.ObjectPutResult, error) {
	req, err := c.newRequest(ctx, http.MethodPut, key, data)
	if err != nil {
		return runtime.ObjectPutResult{}, err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set(checksumMetadataHeader, checksum.String())
	// If-None-Match:* is a best-effort immutability guard; stores that ignore it
	// are still protected by the prior HEAD and the post-conflict verification.
	req.Header.Set("If-None-Match", "*")
	if err := c.sign(req, checksum.String()); err != nil {
		return runtime.ObjectPutResult{}, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return runtime.ObjectPutResult{}, fmt.Errorf("objectstore: put %s: %w", key, err)
	}
	defer drainClose(resp)
	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
		return runtime.ObjectPutResult{
			Version: firstNonEmpty(resp.Header.Get("X-Amz-Version-Id"), strings.Trim(resp.Header.Get("ETag"), `"`)),
		}, nil
	case http.StatusPreconditionFailed, http.StatusConflict:
		return runtime.ObjectPutResult{}, errPreconditionFailed
	default:
		return runtime.ObjectPutResult{}, c.statusError("put", key, resp)
	}
}

func (c *Client) newRequest(ctx context.Context, method, key string, body []byte) (*http.Request, error) {
	target := c.objectURL(key)
	var reader io.Reader
	if body != nil {
		reader = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, target.String(), reader)
	if err != nil {
		return nil, err
	}
	// Assign the pre-built URL so the path is not re-parsed/normalized; the
	// signer canonicalizes it deterministically.
	req.URL = target
	req.Host = target.Host
	if body != nil {
		req.ContentLength = int64(len(body))
	}
	return req, nil
}

func (c *Client) sign(req *http.Request, payloadHash string) error {
	return awsv4.Sign(req, c.service, c.cfg.Region, c.cfg.Credentials, payloadHash, c.clock.Now())
}

func (c *Client) objectURL(key string) *url.URL {
	u := c.bucketURL()
	u.Path = strings.TrimRight(u.Path, "/") + "/" + key
	return u
}

func (c *Client) bucketURL() *url.URL {
	u := *c.base
	if c.cfg.PathStyle {
		u.Path = "/" + c.cfg.Bucket
		return &u
	}
	u.Host = c.cfg.Bucket + "." + u.Host
	u.Path = ""
	return &u
}

func (c *Client) statusError(op, key string, resp *http.Response) error {
	snippet, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
	return fmt.Errorf("objectstore: %s %s: unexpected status %d: %s", op, key, resp.StatusCode, strings.TrimSpace(string(snippet)))
}

func normalizeEndpoint(endpoint string, useTLS bool) (*url.URL, error) {
	if !strings.Contains(endpoint, "://") {
		scheme := "http"
		if useTLS {
			scheme = "https"
		}
		endpoint = scheme + "://" + endpoint
	}
	u, err := url.Parse(endpoint)
	if err != nil {
		return nil, fmt.Errorf("objectstore: invalid endpoint %q: %w", endpoint, err)
	}
	if u.Host == "" {
		return nil, fmt.Errorf("objectstore: endpoint %q has no host", endpoint)
	}
	if useTLS && u.Scheme != "https" {
		return nil, fmt.Errorf("objectstore: TLS requested but endpoint scheme is %q", u.Scheme)
	}
	u.Path = ""
	u.RawQuery = ""
	return u, nil
}

func drainClose(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	_ = resp.Body.Close()
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
