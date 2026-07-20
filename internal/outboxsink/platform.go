package outboxsink

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// PlatformSink pushes canonical, payload-free delivery observations to an
// authenticated platform metadata-ingest endpoint. The wire body is the same
// Observation JSON documented in testdata/metadata-observation.json; this sink
// never imports platform code, so the contract is fixture-based.
type PlatformSink struct {
	url        string
	token      string
	client     *http.Client
	schemaHint string
}

// PlatformOptions configures the platform sink.
type PlatformOptions struct {
	URL   string
	Token string
	// Timeout bounds each publish. Zero selects a safe default.
	Timeout time.Duration
	// InsecureSkipTLSVerify disables TLS verification. It defaults to false and
	// should only be enabled for a local/lab endpoint.
	InsecureSkipTLSVerify bool
}

// NewPlatformSink builds an authenticated platform metadata sink.
func NewPlatformSink(options PlatformOptions) (*PlatformSink, error) {
	if strings.TrimSpace(options.URL) == "" {
		return nil, errors.New("platform metadata sink URL is required")
	}
	if strings.TrimSpace(options.Token) == "" {
		return nil, errors.New("platform metadata sink token is required")
	}
	if !strings.HasPrefix(options.URL, "https://") && !strings.HasPrefix(options.URL, "http://") {
		return nil, errors.New("platform metadata sink URL must be http(s)")
	}
	timeout := options.Timeout
	if timeout <= 0 {
		timeout = 10 * time.Second
	}
	transport := &http.Transport{
		MaxIdleConns:          8,
		MaxIdleConnsPerHost:   4,
		IdleConnTimeout:       30 * time.Second,
		TLSHandshakeTimeout:   timeout,
		ResponseHeaderTimeout: timeout,
		TLSClientConfig: &tls.Config{
			MinVersion:         tls.VersionTLS12,
			InsecureSkipVerify: options.InsecureSkipTLSVerify,
		},
	}
	return &PlatformSink{
		url:        options.URL,
		token:      options.Token,
		schemaHint: "1",
		client: &http.Client{
			Timeout:   timeout,
			Transport: transport,
			CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
				return errors.New("metadata sink refuses redirects")
			},
		},
	}, nil
}

// Publish POSTs one observation to the platform endpoint with bearer auth. A
// non-2xx response is an error so the worker reschedules the outbox row.
func (s *PlatformSink) Publish(ctx context.Context, observation Observation) error {
	if observation.SchemaVersion == "" {
		observation.SchemaVersion = s.schemaHint
	}
	body, err := json.Marshal(observation)
	if err != nil {
		return fmt.Errorf("encode observation: %w", err)
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, s.url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("build metadata request: %w", err)
	}
	request.Header.Set("Content-Type", "application/json")
	request.Header.Set("Authorization", "Bearer "+s.token)
	request.Header.Set("Hookship-Metadata-Schema", observation.SchemaVersion)

	response, err := s.client.Do(request)
	if err != nil {
		return fmt.Errorf("post observation: %w", err)
	}
	defer func() { _ = response.Body.Close() }()
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return fmt.Errorf("metadata sink returned HTTP %d", response.StatusCode)
	}
	return nil
}

// Name identifies the sink.
func (s *PlatformSink) Name() string { return "platform" }

// Close releases idle connections.
func (s *PlatformSink) Close() error {
	s.client.CloseIdleConnections()
	return nil
}
