// Package awsv4 implements AWS Signature Version 4 request signing using only
// the Go standard library. It is deliberately small and dependency-free: the
// object-store PayloadStore (S3-compatible) and the AWS KMS key provider both
// sign requests through this one signer, so there is no third-party AWS SDK on
// the delivery data plane.
//
// The signer follows the published SigV4 algorithm: it builds a canonical
// request, derives a scoped signing key with a chained HMAC, and writes the
// Authorization header. Callers supply the payload hash explicitly so both
// signed-payload (S3 PUT, KMS) and streaming uses are supported without the
// signer buffering bodies.
package awsv4

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"sort"
	"strings"
	"time"
)

// Algorithm is the SigV4 algorithm identifier.
const Algorithm = "AWS4-HMAC-SHA256"

// EmptyPayloadHash is the hex SHA-256 of an empty body, used for GET/DELETE and
// other bodyless signed requests.
const EmptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// UnsignedPayload marks a request whose body is intentionally excluded from the
// signature (for large streamed uploads). The literal is part of the SigV4
// contract.
const UnsignedPayload = "UNSIGNED-PAYLOAD"

// Credentials are the static AWS credentials used to sign a request. A session
// token is optional and only present for temporary credentials.
type Credentials struct {
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

func (c Credentials) valid() bool {
	return strings.TrimSpace(c.AccessKeyID) != "" && strings.TrimSpace(c.SecretAccessKey) != ""
}

// Configured reports whether both the access key ID and secret access key are
// present, so callers can fail closed before constructing a signed client.
func (c Credentials) Configured() bool { return c.valid() }

// PayloadHash returns the hex SHA-256 of body, or EmptyPayloadHash when body is
// empty. It is a convenience for callers that hold the full body in memory.
func PayloadHash(body []byte) string {
	if len(body) == 0 {
		return EmptyPayloadHash
	}
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}

// Sign adds the x-amz-date, x-amz-content-sha256, optional x-amz-security-token,
// and Authorization headers to req for the given service and region. payloadHash
// must be the hex SHA-256 of the request body (or UnsignedPayload). The request
// Host header is derived from req.URL when unset. now fixes the signing time so
// tests are deterministic.
func Sign(req *http.Request, service, region string, creds Credentials, payloadHash string, now time.Time) error {
	if req == nil {
		return errors.New("awsv4: request is required")
	}
	if strings.TrimSpace(service) == "" || strings.TrimSpace(region) == "" {
		return errors.New("awsv4: service and region are required")
	}
	if !creds.valid() {
		return errors.New("awsv4: access key ID and secret access key are required")
	}
	if payloadHash == "" {
		payloadHash = EmptyPayloadHash
	}

	amzDate := now.UTC().Format("20060102T150405Z")
	dateStamp := now.UTC().Format("20060102")

	host := req.Host
	if host == "" {
		host = req.URL.Host
	}
	req.Header.Set("Host", host)
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	if creds.SessionToken != "" {
		req.Header.Set("X-Amz-Security-Token", creds.SessionToken)
	}

	canonicalHeaderBlock, signedHeaders := canonicalHeaders(req, host)
	canonicalRequest := strings.Join([]string{
		req.Method,
		canonicalURI(req.URL.EscapedPath()),
		canonicalQuery(req.URL.RawQuery),
		canonicalHeaderBlock,
		signedHeaders,
		payloadHash,
	}, "\n")

	scope := strings.Join([]string{dateStamp, region, service, "aws4_request"}, "/")
	stringToSign := strings.Join([]string{
		Algorithm,
		amzDate,
		scope,
		hexSHA256([]byte(canonicalRequest)),
	}, "\n")

	signingKey := deriveSigningKey(creds.SecretAccessKey, dateStamp, region, service)
	signature := hex.EncodeToString(hmacSHA256(signingKey, []byte(stringToSign)))

	authorization := Algorithm +
		" Credential=" + creds.AccessKeyID + "/" + scope +
		", SignedHeaders=" + signedHeaders +
		", Signature=" + signature
	req.Header.Set("Authorization", authorization)
	return nil
}

// canonicalHeaders builds the canonical header block and the signed-headers
// list. Host, Content-Type, and every x-amz-* header present are signed.
func canonicalHeaders(req *http.Request, host string) (string, string) {
	values := map[string][]string{"host": {host}}
	for name, vs := range req.Header {
		lower := strings.ToLower(name)
		if lower == "host" || lower == "authorization" {
			continue
		}
		if lower == "content-type" || strings.HasPrefix(lower, "x-amz-") {
			values[lower] = vs
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)

	var builder strings.Builder
	for _, name := range names {
		trimmed := make([]string, 0, len(values[name]))
		for _, v := range values[name] {
			trimmed = append(trimmed, collapseSpaces(v))
		}
		builder.WriteString(name)
		builder.WriteByte(':')
		builder.WriteString(strings.Join(trimmed, ","))
		builder.WriteByte('\n')
	}
	return builder.String(), strings.Join(names, ";")
}

// canonicalURI encodes the path per SigV4. Amazon S3 paths are not normalized;
// each segment is URI-encoded but the slashes between segments are preserved.
func canonicalURI(path string) string {
	if path == "" {
		return "/"
	}
	segments := strings.Split(path, "/")
	for i, seg := range segments {
		// EscapedPath already percent-encodes; re-encode from the decoded form to
		// guarantee SigV4's uppercase-hex, unreserved-only rule.
		segments[i] = uriEncode(decodePercent(seg), false)
	}
	return strings.Join(segments, "/")
}

// canonicalQuery re-encodes and sorts the query string per SigV4.
func canonicalQuery(raw string) string {
	if raw == "" {
		return ""
	}
	type kv struct{ k, v string }
	var pairs []kv
	for _, part := range strings.Split(raw, "&") {
		if part == "" {
			continue
		}
		key, value, _ := strings.Cut(part, "=")
		pairs = append(pairs, kv{
			k: uriEncode(decodePercent(key), true),
			v: uriEncode(decodePercent(value), true),
		})
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k == pairs[j].k {
			return pairs[i].v < pairs[j].v
		}
		return pairs[i].k < pairs[j].k
	})
	out := make([]string, len(pairs))
	for i, p := range pairs {
		out[i] = p.k + "=" + p.v
	}
	return strings.Join(out, "&")
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

func hmacSHA256(key, data []byte) []byte {
	mac := hmac.New(sha256.New, key)
	mac.Write(data)
	return mac.Sum(nil)
}

func hexSHA256(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// collapseSpaces trims a header value and collapses runs of internal spaces to a
// single space, as SigV4 requires for canonical header values.
func collapseSpaces(value string) string {
	value = strings.TrimSpace(value)
	if !strings.Contains(value, "  ") {
		return value
	}
	var builder strings.Builder
	space := false
	for _, r := range value {
		if r == ' ' {
			if space {
				continue
			}
			space = true
		} else {
			space = false
		}
		builder.WriteRune(r)
	}
	return builder.String()
}

const upperHex = "0123456789ABCDEF"

// uriEncode percent-encodes s per RFC 3986, keeping unreserved characters. When
// encodeSlash is false, forward slashes are preserved (S3 path segments).
func uriEncode(s string, encodeSlash bool) string {
	var builder strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c >= 'A' && c <= 'Z', c >= 'a' && c <= 'z', c >= '0' && c <= '9',
			c == '-', c == '_', c == '.', c == '~':
			builder.WriteByte(c)
		case c == '/' && !encodeSlash:
			builder.WriteByte(c)
		default:
			builder.WriteByte('%')
			builder.WriteByte(upperHex[c>>4])
			builder.WriteByte(upperHex[c&15])
		}
	}
	return builder.String()
}

// decodePercent decodes percent-escapes so a value can be canonically
// re-encoded. Invalid escapes are passed through unchanged.
func decodePercent(s string) string {
	if !strings.Contains(s, "%") {
		return s
	}
	var builder strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '%' && i+2 < len(s) {
			hi := fromHex(s[i+1])
			lo := fromHex(s[i+2])
			if hi >= 0 && lo >= 0 {
				builder.WriteByte(byte(hi<<4 | lo))
				i += 2
				continue
			}
		}
		builder.WriteByte(s[i])
	}
	return builder.String()
}

func fromHex(c byte) int {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0')
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10
	default:
		return -1
	}
}
