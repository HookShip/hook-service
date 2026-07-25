package ingest

import (
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/hookship/hook-service/internal/runtime"
)

// transportAllowed enforces that non-loopback plaintext requests fail closed
// when secure transport is required.
func (h *Handler) transportAllowed(r *http.Request) bool {
	if r.TLS != nil || h.options.TrustProxy {
		return true
	}
	if !h.options.RequireSecureTransport {
		return true
	}
	return isLoopbackRequest(r)
}

// assignmentTransportAllowed rejects evaluation credentials used over a
// non-loopback plaintext connection unless a trusted proxy boundary is asserted.
func (h *Handler) assignmentTransportAllowed(assignment runtime.Assignment, r *http.Request) bool {
	if assignment.Environment != runtime.EnvironmentEvaluation {
		return true
	}
	if r.TLS != nil || h.options.TrustProxy {
		return true
	}
	return isLoopbackRequest(r)
}

func isLoopbackRequest(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	ip := net.ParseIP(strings.TrimSpace(host))
	return ip != nil && ip.IsLoopback()
}

func (h *Handler) readBody(w http.ResponseWriter, r *http.Request, requestID string, limit int64) ([]byte, bool) {
	if limit <= 0 {
		limit = 1
	}
	reader := http.MaxBytesReader(w, r.Body, limit)
	body, err := io.ReadAll(reader)
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			writeProblem(w, r, codePayloadTooLarge, requestID, map[string]string{
				"Hookship-Max-Body-Bytes": itoa(limit),
			})
			return nil, false
		}
		writeProblem(w, r, codeMalformedRequest, requestID, nil)
		return nil, false
	}
	return body, true
}

type signedHeaderSet struct {
	timestamp      string
	nonce          string
	idempotencyKey string
	eventID        string
	eventType      string
	route          string
	contentType    string
	bodyHashHex    string
}

func (h *Handler) collectSignedHeaders(w http.ResponseWriter, r *http.Request, requestID string) (signedHeaderSet, bool) {
	fields := map[string]*string{}
	set := signedHeaderSet{}
	lookups := []struct {
		name        string
		destination *string
	}{
		{"X-Hookship-Timestamp", &set.timestamp},
		{"X-Hookship-Nonce", &set.nonce},
		{"Idempotency-Key", &set.idempotencyKey},
		{"X-Hookship-Event-ID", &set.eventID},
		{"X-Hookship-Event-Type", &set.eventType},
		{"X-Hookship-Route", &set.route},
		{"X-Hookship-Body-SHA256", &set.bodyHashHex},
		{"Content-Type", &set.contentType},
	}
	for _, lookup := range lookups {
		value, present, duplicate := singleHeader(r.Header, lookup.name)
		if duplicate {
			writeProblem(w, r, codeMalformedRequest, requestID, nil)
			return signedHeaderSet{}, false
		}
		if !present {
			writeProblem(w, r, codeMalformedRequest, requestID, nil)
			return signedHeaderSet{}, false
		}
		*lookup.destination = value
		fields[lookup.name] = lookup.destination
	}
	if len(set.nonce) == 0 || len(set.nonce) > maxNonceSize {
		writeProblem(w, r, codeMalformedRequest, requestID, nil)
		return signedHeaderSet{}, false
	}
	return set, true
}

// singleHeader returns the trimmed single value of a header, whether it was
// present, and whether it appeared more than once (which is rejected).
func singleHeader(header http.Header, name string) (value string, present bool, duplicate bool) {
	values := header.Values(name)
	switch len(values) {
	case 0:
		return "", false, false
	case 1:
		return strings.TrimSpace(values[0]), true, false
	default:
		return "", true, true
	}
}

func validEventHeaders(headers signedHeaderSet) bool {
	if !eventIDPattern.MatchString(headers.eventID) || len(headers.eventID) > 128 {
		return false
	}
	if !eventTypePattern.MatchString(headers.eventType) || len(headers.eventType) > 128 {
		return false
	}
	if !eventTypePattern.MatchString(headers.route) || len(headers.route) > 128 {
		return false
	}
	if len(headers.contentType) < 3 || len(headers.contentType) > 255 || strings.ContainsAny(headers.contentType, "\r\n") {
		return false
	}
	return true
}

func itoa(value int64) string {
	digits := []byte{}
	if value == 0 {
		return "0"
	}
	for value > 0 {
		digits = append([]byte{byte('0' + value%10)}, digits...)
		value /= 10
	}
	return string(digits)
}
