package keyprovider

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/payloadcrypto"
	"github.com/hookship/hook-service/internal/runtime"
)

// KMSConfig configures the AWS-compatible KMS key provider. Credentials and the
// wrapped data key are never logged.
type KMSConfig struct {
	KeyID          string
	Region         string
	Endpoint       string // optional override for local/fake KMS; default AWS
	Credentials    awsv4.Credentials
	WrappedDataKey string // base64 KMS-wrapped DEK, stable across the cell
	Timeout        time.Duration
	Clock          runtime.Clock

	// transportRT is a test seam. When nil a real HTTP transport is used.
	transportRT http.RoundTripper
}

// KMSProvider resolves the payload DEK by unwrapping a KMS-encrypted data key.
type KMSProvider struct {
	client  *kmsClient
	keyID   string
	wrapped []byte

	mu       sync.Mutex
	resolved *Material
}

var _ Provider = (*KMSProvider)(nil)

// NewKMS validates the configuration and constructs a KMS provider. The wrapped
// data key is required and must decode from base64; it is unwrapped lazily on
// the first Active call so construction never blocks on the network.
func NewKMS(cfg KMSConfig) (*KMSProvider, error) {
	if strings.TrimSpace(cfg.KeyID) == "" {
		return nil, errors.New("kms: key ID is required")
	}
	if strings.TrimSpace(cfg.WrappedDataKey) == "" {
		return nil, errors.New("kms: a wrapped data key is required for a stable cell DEK")
	}
	wrapped, err := base64.StdEncoding.DecodeString(cfg.WrappedDataKey)
	if err != nil || len(wrapped) == 0 {
		return nil, errors.New("kms: wrapped data key must be non-empty base64")
	}
	client, err := newKMSClient(cfg)
	if err != nil {
		return nil, err
	}
	return &KMSProvider{client: client, keyID: cfg.KeyID, wrapped: wrapped}, nil
}

// Name identifies the provider.
func (p *KMSProvider) Name() string { return "aws-kms" }

// Active unwraps the DEK via KMS Decrypt (cached after the first success) and
// returns stable DEK material. The reference intentionally omits the rotating
// KMS key ID so a rewrap does not change the payload-bound identity.
func (p *KMSProvider) Active(ctx context.Context) (Material, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.resolved != nil {
		return *p.resolved, nil
	}
	plaintext, err := p.client.decrypt(ctx, p.wrapped)
	if err != nil {
		return Material{}, err
	}
	if len(plaintext) != payloadcrypto.KeySize {
		return Material{}, fmt.Errorf("kms: unwrapped data key must be %d bytes, got %d", payloadcrypto.KeySize, len(plaintext))
	}
	material := Material{
		Key:       plaintext,
		Reference: "kms://cell/dek",
		Version:   deriveVersion(plaintext),
	}
	p.resolved = &material
	return material, nil
}

// Rewrap re-encrypts the current DEK plaintext under newKeyID (or the provider's
// key when newKeyID is empty) and returns a new base64 wrapped data key. The DEK
// plaintext is unchanged, so previously sealed payloads remain decryptable and
// their exact bytes are untouched; this is the operator rotation primitive.
func (p *KMSProvider) Rewrap(ctx context.Context, newKeyID string) (string, error) {
	material, err := p.Active(ctx)
	if err != nil {
		return "", err
	}
	keyID := strings.TrimSpace(newKeyID)
	if keyID == "" {
		keyID = p.keyID
	}
	wrapped, err := p.client.encrypt(ctx, keyID, material.Key)
	if err != nil {
		return "", err
	}
	return base64.StdEncoding.EncodeToString(wrapped), nil
}

// kmsClient is a minimal AWS KMS JSON client using SigV4 over net/http.
type kmsClient struct {
	endpoint string
	region   string
	creds    awsv4.Credentials
	http     *http.Client
	clock    runtime.Clock
}

func newKMSClient(cfg KMSConfig) (*kmsClient, error) {
	if strings.TrimSpace(cfg.Region) == "" {
		return nil, errors.New("kms: region is required")
	}
	if !cfg.Credentials.Configured() {
		return nil, errors.New("kms: credentials are required")
	}
	endpoint := strings.TrimSpace(cfg.Endpoint)
	if endpoint == "" {
		endpoint = "https://kms." + cfg.Region + ".amazonaws.com"
	}
	if !strings.Contains(endpoint, "://") {
		endpoint = "https://" + endpoint
	}
	timeout := cfg.Timeout
	if timeout <= 0 {
		timeout = 15 * time.Second
	}
	clock := cfg.Clock
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	transport := cfg.transportRT
	if transport == nil {
		transport = http.DefaultTransport
	}
	return &kmsClient{
		endpoint: strings.TrimRight(endpoint, "/"),
		region:   cfg.Region,
		creds:    cfg.Credentials,
		http:     &http.Client{Timeout: timeout, Transport: transport},
		clock:    clock,
	}, nil
}

func (c *kmsClient) decrypt(ctx context.Context, ciphertext []byte) ([]byte, error) {
	var out struct {
		Plaintext string `json:"Plaintext"`
	}
	if err := c.call(ctx, "Decrypt", map[string]any{
		"CiphertextBlob": base64.StdEncoding.EncodeToString(ciphertext),
	}, &out); err != nil {
		return nil, err
	}
	plaintext, err := base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return nil, fmt.Errorf("kms: decode decrypt plaintext: %w", err)
	}
	return plaintext, nil
}

func (c *kmsClient) encrypt(ctx context.Context, keyID string, plaintext []byte) ([]byte, error) {
	var out struct {
		CiphertextBlob string `json:"CiphertextBlob"`
	}
	if err := c.call(ctx, "Encrypt", map[string]any{
		"KeyId":     keyID,
		"Plaintext": base64.StdEncoding.EncodeToString(plaintext),
	}, &out); err != nil {
		return nil, err
	}
	blob, err := base64.StdEncoding.DecodeString(out.CiphertextBlob)
	if err != nil {
		return nil, fmt.Errorf("kms: decode encrypt blob: %w", err)
	}
	return blob, nil
}

// GenerateDataKey provisions a fresh DEK wrapped by the provider's KMS key and
// returns the base64 wrapped blob. It is a bootstrap/rotation helper (used by
// operator tooling and tests); the runtime consumes a stable wrapped key.
func (c *kmsClient) generateDataKey(ctx context.Context, keyID string) (wrapped, plaintext []byte, err error) {
	var out struct {
		CiphertextBlob string `json:"CiphertextBlob"`
		Plaintext      string `json:"Plaintext"`
	}
	if err := c.call(ctx, "GenerateDataKey", map[string]any{
		"KeyId":   keyID,
		"KeySpec": "AES_256",
	}, &out); err != nil {
		return nil, nil, err
	}
	wrapped, err = base64.StdEncoding.DecodeString(out.CiphertextBlob)
	if err != nil {
		return nil, nil, fmt.Errorf("kms: decode data key blob: %w", err)
	}
	plaintext, err = base64.StdEncoding.DecodeString(out.Plaintext)
	if err != nil {
		return nil, nil, fmt.Errorf("kms: decode data key plaintext: %w", err)
	}
	return wrapped, plaintext, nil
}

func (c *kmsClient) call(ctx context.Context, operation string, payload map[string]any, out any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("kms: marshal %s: %w", operation, err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.endpoint+"/", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-amz-json-1.1")
	req.Header.Set("X-Amz-Target", "TrentService."+operation)
	req.ContentLength = int64(len(body))
	if err := awsv4.Sign(req, "kms", c.region, c.creds, awsv4.PayloadHash(body), c.clock.Now()); err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("kms: %s: %w", operation, err)
	}
	defer func() {
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
	}()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return fmt.Errorf("kms: read %s response: %w", operation, err)
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("kms: %s failed: status %d: %s", operation, resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("kms: decode %s response: %w", operation, err)
	}
	return nil
}
