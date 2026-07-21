package ingest

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"
)

// canonicalPrefix is the protocol-v1 domain separator for request signing.
const canonicalPrefix = "hookship-hmac-sha256-v1"

// TimestampLayout is the exact accepted timestamp format. Fractional seconds,
// offsets, lowercase z, and omitted seconds are invalid.
const TimestampLayout = "2006-01-02T15:04:05Z"

// ReplayWindow bounds acceptable timestamp skew for inbound requests.
const ReplayWindow = 300 * time.Second

// SignedRequest carries the exact values bound into the canonical string.
type SignedRequest struct {
	Method         string
	Path           string
	CredentialID   string
	Timestamp      string
	Nonce          string
	IdempotencyKey string
	EventID        string
	EventType      string
	Route          string
	ContentType    string
	BodySHA256Hex  string
}

// Canonical builds the twelve-line LF-separated canonical string with no final
// LF, exactly as defined by spec/v1/signing.md.
func Canonical(request SignedRequest) string {
	var builder strings.Builder
	builder.WriteString(canonicalPrefix)
	for _, line := range [][2]string{
		{"method", strings.ToUpper(request.Method)},
		{"path", request.Path},
		{"credential-id", request.CredentialID},
		{"timestamp", request.Timestamp},
		{"nonce", request.Nonce},
		{"idempotency-key", request.IdempotencyKey},
		{"event-id", request.EventID},
		{"event-type", request.EventType},
		{"route", request.Route},
		{"content-type", request.ContentType},
		{"body-sha256", request.BodySHA256Hex},
	} {
		builder.WriteByte('\n')
		builder.WriteString(line[0])
		builder.WriteByte(':')
		builder.WriteString(line[1])
	}
	return builder.String()
}

// ParseAuthorization extracts the credential ID and base64url signature from an
// `Authorization: Hookship-HMAC Credential=<id>,Signature=<sig>` header. It
// rejects control characters, commas in values, and malformed structure.
func ParseAuthorization(header string) (credentialID, signature string, err error) {
	const scheme = "Hookship-HMAC "
	if !strings.HasPrefix(header, scheme) {
		return "", "", errors.New("authorization scheme must be Hookship-HMAC")
	}
	parameters := strings.TrimPrefix(header, scheme)
	if strings.ContainsAny(parameters, "\r\n\t") {
		return "", "", errors.New("authorization contains control characters")
	}
	parts := strings.Split(parameters, ",")
	if len(parts) != 2 {
		return "", "", errors.New("authorization must contain exactly two parameters")
	}
	for _, part := range parts {
		key, value, found := strings.Cut(part, "=")
		if !found {
			return "", "", errors.New("authorization parameter is malformed")
		}
		key = strings.TrimSpace(key)
		switch key {
		case "Credential":
			credentialID = value
		case "Signature":
			signature = value
		default:
			return "", "", fmt.Errorf("unknown authorization parameter %q", key)
		}
	}
	if credentialID == "" || signature == "" {
		return "", "", errors.New("authorization is missing Credential or Signature")
	}
	return credentialID, signature, nil
}

// VerifySignature verifies the HMAC over the canonical string with strict
// canonical unpadded base64url decoding and constant-time comparison. A
// non-canonical signature (non-zero unused trailing bits) is rejected.
func VerifySignature(secret []byte, request SignedRequest, signature string) error {
	decoded, err := base64.RawURLEncoding.DecodeString(signature)
	if err != nil {
		return errors.New("signature is not valid base64url")
	}
	if len(decoded) != sha256.Size {
		return errors.New("signature must decode to 32 bytes")
	}
	if base64.RawURLEncoding.EncodeToString(decoded) != signature {
		return errors.New("signature is not canonical base64url")
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(Canonical(request)))
	expected := mac.Sum(nil)
	if subtle.ConstantTimeCompare(expected, decoded) != 1 {
		return errors.New("signature does not match")
	}
	return nil
}

// ComputeSignature returns the canonical unpadded base64url signature for a
// request. It backs test vectors and the reference client.
func ComputeSignature(secret []byte, request SignedRequest) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(Canonical(request)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ValidateTimestamp enforces the exact format, a real UTC instant, and the
// replay window around the server clock.
func ValidateTimestamp(value string, now time.Time, window time.Duration) (time.Time, error) {
	if len(value) != len("2006-01-02T15:04:05Z") {
		return time.Time{}, errors.New("timestamp has an invalid length")
	}
	parsed, err := time.Parse(TimestampLayout, value)
	if err != nil {
		return time.Time{}, errors.New("timestamp is not a valid UTC instant")
	}
	if parsed.Format(TimestampLayout) != value {
		return time.Time{}, errors.New("timestamp is not canonical")
	}
	skew := now.Sub(parsed)
	if skew < 0 {
		skew = -skew
	}
	if skew > window {
		return time.Time{}, fmt.Errorf("timestamp is outside the %s replay window", window)
	}
	return parsed, nil
}
