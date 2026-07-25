// Package signing implements Standard Webhooks-compatible outbound signing over
// the exact payload bytes. The signing secret is scoped to one immutable
// endpoint/assignment configuration and never logged.
//
// The scheme matches the Standard Webhooks specification
// (https://www.standardwebhooks.com): the signed content is
// "<msg-id>.<unix-timestamp>.<payload>", the signature is
// base64(HMAC-SHA256(secret, content)), and the wire header carries one or more
// space-separated versioned signatures of the form "v1,<signature>".
package signing

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Header names used by Standard Webhooks. They are lowercase on the wire; Go's
// net/http canonicalizes them on send, and receivers compare case-insensitively.
const (
	HeaderID        = "webhook-id"
	HeaderTimestamp = "webhook-timestamp"
	HeaderSignature = "webhook-signature"

	// SecretPrefix marks a base64-encoded Standard Webhooks secret.
	SecretPrefix = "whsec_"

	// Version is the only signature scheme version emitted by this signer.
	Version = "v1"
)

// DefaultTolerance bounds acceptable clock skew when verifying a timestamp.
const DefaultTolerance = 5 * time.Minute

// Signer signs outbound deliveries with one endpoint-scoped secret.
type Signer struct {
	key []byte
}

// Signature is the set of headers to attach to an outbound delivery.
type Signature struct {
	ID        string
	Timestamp string
	Value     string
}

// Headers returns the Standard Webhooks headers for the signature.
func (s Signature) Headers() map[string]string {
	return map[string]string{
		HeaderID:        s.ID,
		HeaderTimestamp: s.Timestamp,
		HeaderSignature: s.Value,
	}
}

// NewSigner builds a signer from a Standard Webhooks secret. A "whsec_" prefix
// selects base64 decoding of the remainder; otherwise the raw bytes are used as
// the HMAC key.
func NewSigner(secret []byte) (*Signer, error) {
	key, err := DecodeSecret(secret)
	if err != nil {
		return nil, err
	}
	if len(key) == 0 {
		return nil, errors.New("signing secret must not be empty")
	}
	return &Signer{key: key}, nil
}

// DecodeSecret resolves the raw HMAC key from a Standard Webhooks secret.
func DecodeSecret(secret []byte) ([]byte, error) {
	text := string(secret)
	if strings.HasPrefix(text, SecretPrefix) {
		encoded := strings.TrimPrefix(text, SecretPrefix)
		decoded, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil {
			return nil, fmt.Errorf("decode whsec secret: %w", err)
		}
		return decoded, nil
	}
	key := make([]byte, len(secret))
	copy(key, secret)
	return key, nil
}

// Sign produces the Standard Webhooks signature over the exact payload bytes.
// The message ID is the canonical delivery identity and the timestamp is the
// attempt time; both are bound into the signed content.
func (s *Signer) Sign(messageID string, timestamp time.Time, payload []byte) (Signature, error) {
	if messageID == "" {
		return Signature{}, errors.New("message ID is required")
	}
	if strings.ContainsAny(messageID, ".\r\n ") {
		return Signature{}, errors.New("message ID must not contain a period, space, or newline")
	}
	unix := strconv.FormatInt(timestamp.UTC().Unix(), 10)
	value := s.computeVersioned(messageID, unix, payload)
	return Signature{ID: messageID, Timestamp: unix, Value: value}, nil
}

func (s *Signer) computeVersioned(messageID, unix string, payload []byte) string {
	return Version + "," + s.computeRaw(messageID, unix, payload)
}

func (s *Signer) computeRaw(messageID, unix string, payload []byte) string {
	mac := hmac.New(sha256.New, s.key)
	// SignedContent is the single canonical-content constructor; using it here
	// guarantees the HMAC input is byte-identical to the exported conformance
	// bytes. Byte identity is locked by the Standard Webhooks golden vectors.
	mac.Write(SignedContent(messageID, unix, payload))
	return base64.StdEncoding.EncodeToString(mac.Sum(nil))
}

// SignedContent returns the exact bytes the HMAC is computed over. It is
// exported for conformance vectors and cross-implementation checks.
func SignedContent(messageID, unixTimestamp string, payload []byte) []byte {
	content := make([]byte, 0, len(messageID)+len(unixTimestamp)+len(payload)+2)
	content = append(content, messageID...)
	content = append(content, '.')
	content = append(content, unixTimestamp...)
	content = append(content, '.')
	content = append(content, payload...)
	return content
}

// Verify checks a wire signature header against the payload using constant-time
// comparison. It accepts a space-separated list of versioned signatures and
// tolerates configured clock skew. It is used by the reference sink and tests.
func (s *Signer) Verify(messageID, unixTimestamp, header string, payload []byte, tolerance time.Duration) error {
	if tolerance <= 0 {
		tolerance = DefaultTolerance
	}
	seconds, err := strconv.ParseInt(strings.TrimSpace(unixTimestamp), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid timestamp: %w", err)
	}
	skew := time.Since(time.Unix(seconds, 0))
	if skew < 0 {
		skew = -skew
	}
	if skew > tolerance {
		return fmt.Errorf("timestamp outside tolerance of %s", tolerance)
	}

	expected := s.computeRaw(messageID, strings.TrimSpace(unixTimestamp), payload)
	expectedBytes, err := base64.StdEncoding.DecodeString(expected)
	if err != nil {
		return fmt.Errorf("encode expected signature: %w", err)
	}
	for _, candidate := range strings.Fields(header) {
		parts := strings.SplitN(candidate, ",", 2)
		if len(parts) != 2 || parts[0] != Version {
			continue
		}
		candidateBytes, err := base64.StdEncoding.DecodeString(parts[1])
		if err != nil {
			continue
		}
		if hmac.Equal(expectedBytes, candidateBytes) {
			return nil
		}
	}
	return errors.New("no matching v1 signature")
}
