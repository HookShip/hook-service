// Package ingest implements the protocol-v1 producer data plane: HMAC request
// authentication over the exact raw body, nonce replay protection, assignment
// authorization, idempotent acceptance across the durability barrier, and the
// payload-free status read. It never parses or logs the raw body.
package ingest

import (
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

const (
	ingestPath   = "/v1/event-ingests"
	nonceRetain  = 600 * time.Second
	maxNonceSize = 512
)

var (
	eventIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	eventTypePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
	ingestIDPattern  = regexp.MustCompile(`^ing_[A-Z0-9]{26}$`)
)

// Options configures the ingest handler.
type Options struct {
	Backend Backend
	Clock   runtime.Clock
	IDs     runtime.IDSource
	Logger  *slog.Logger
	// RequireSecureTransport rejects non-loopback plaintext requests. It is set
	// for durable/production profiles; the evaluation profile allows loopback
	// plaintext only.
	RequireSecureTransport bool
	// EvaluationMode marks the process as evaluation; evaluation assignments may
	// authenticate only over loopback or TLS.
	EvaluationMode bool
	// TrustProxy asserts that a trusted boundary (loopback-only Docker publish,
	// SSH tunnel, or reverse proxy) terminates external exposure. It relaxes the
	// evaluation loopback-remote check because the operator owns the boundary.
	TrustProxy bool
}

// Handler serves the protocol-v1 ingest endpoints.
type Handler struct {
	backend Backend
	clock   runtime.Clock
	ids     runtime.IDSource
	logger  *slog.Logger
	options Options
}

// New validates options and builds a handler.
func New(options Options) (*Handler, error) {
	if options.Backend == nil {
		return nil, errors.New("ingest handler requires a backend")
	}
	if options.Clock == nil {
		options.Clock = runtime.SystemClock{}
	}
	if options.IDs == nil {
		options.IDs = runtime.NewULIDSource(options.Clock)
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Handler{
		backend: options.Backend,
		clock:   options.Clock,
		ids:     options.IDs,
		logger:  options.Logger,
		options: options,
	}, nil
}

// Register attaches the ingest routes to a mux using method-aware patterns.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.Handle("POST "+ingestPath, http.HandlerFunc(h.handlePost))
	mux.Handle("GET "+ingestPath+"/{ingestID}", http.HandlerFunc(h.handleGet))
}

func (h *Handler) handlePost(w http.ResponseWriter, r *http.Request) {
	requestID := h.ids.NewID("req")
	now := h.clock.Now()

	if !h.transportAllowed(r) {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	if encoding := r.Header.Get("Content-Encoding"); encoding != "" && !strings.EqualFold(encoding, "identity") {
		writeProblem(w, r, codeUnsupportedEncoding, requestID, nil)
		return
	}

	headers, ok := h.collectSignedHeaders(w, r, requestID)
	if !ok {
		return
	}
	credentialID, signature, err := ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	if _, err := ValidateTimestamp(headers.timestamp, now, ReplayWindow); err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}

	assignment, err := h.backend.ResolveCredential(r.Context(), credentialID)
	if err != nil {
		// Unknown credentials are indistinguishable from signature failures.
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	if !h.assignmentTransportAllowed(assignment, r) {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}

	body, ok := h.readBody(w, r, requestID, assignment.MaxBodyBytes)
	if !ok {
		return
	}
	bodyHash := runtime.SHA256(body)
	if bodyHash.String() != headers.bodyHashHex {
		writeProblem(w, r, codeBodyHashMismatch, requestID, nil)
		return
	}

	signed := SignedRequest{
		Method:         http.MethodPost,
		Path:           ingestPath,
		CredentialID:   credentialID,
		Timestamp:      headers.timestamp,
		Nonce:          headers.nonce,
		IdempotencyKey: headers.idempotencyKey,
		EventID:        headers.eventID,
		EventType:      headers.eventType,
		Route:          headers.route,
		ContentType:    headers.contentType,
		BodySHA256Hex:  headers.bodyHashHex,
	}
	if err := VerifySignature(assignment.HMACSecret, signed, signature); err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}

	if !validEventHeaders(headers) {
		writeProblem(w, r, codeInvalidEvent, requestID, nil)
		return
	}
	if !assignment.AllowsRoute(headers.route) {
		writeProblem(w, r, codeAssignmentForbidden, requestID, nil)
		return
	}

	stable := runtime.StableHeaders{
		EventID:     headers.eventID,
		EventType:   headers.eventType,
		Route:       headers.route,
		ContentType: headers.contentType,
	}

	// Reserve the replay nonce before applying idempotency.
	retainUntil := now.Add(nonceRetain)
	if signedAt, err := time.Parse(TimestampLayout, headers.timestamp); err == nil {
		if candidate := signedAt.Add(nonceRetain); candidate.After(retainUntil) {
			retainUntil = candidate
		}
	}
	fresh, err := h.backend.ReserveNonce(r.Context(), credentialID, headers.nonce, now, retainUntil)
	if err != nil {
		h.serverError(w, r, requestID, "reserve nonce", err)
		return
	}
	if !fresh {
		writeProblem(w, r, codeReplayDetected, requestID, nil)
		return
	}

	ledgerID := h.ids.NewID("led")
	reservation, err := h.backend.ReserveIdempotency(r.Context(), runtime.ReservationRequest{
		LedgerID:       ledgerID,
		AssignmentID:   assignment.ID,
		IdempotencyKey: headers.idempotencyKey,
		BodySHA256:     bodyHash,
		Headers:        stable,
		ReservedAt:     now,
	})
	if errors.Is(err, runtime.ErrCapacity) {
		writeProblem(w, r, codeIngestUnavailable, requestID, retryAfterHeader(5))
		return
	}
	if err != nil {
		h.serverError(w, r, requestID, "reserve idempotency", err)
		return
	}

	switch reservation.Status {
	case runtime.ReservationConflict:
		writeProblem(w, r, codeIdempotencyConflict, requestID, nil)
		return
	case runtime.ReservationMetadataConflict:
		writeProblem(w, r, codeIdempotencyMetadata, requestID, nil)
		return
	case runtime.ReservationExpired:
		writeProblem(w, r, codeIdempotencyExpired, requestID, nil)
		return
	case runtime.ReservationDuplicate:
		h.writeDuplicateReceipt(w, r, requestID, reservation.Reservation, stable, headers.bodyHashHex)
		return
	}

	h.acceptAndRespond(w, r, requestID, now, assignment, reservation.Reservation, stable, bodyHash, body)
}

func (h *Handler) acceptAndRespond(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	now time.Time,
	assignment runtime.Assignment,
	reservation runtime.Reservation,
	stable runtime.StableHeaders,
	bodyHash runtime.Hash,
	body []byte,
) {
	sealed, err := h.backend.Seal(body)
	if err != nil {
		h.serverError(w, r, requestID, "seal payload", err)
		return
	}
	acceptedAt := now.Truncate(time.Second)
	retryHorizon := assignment.RetryHorizon
	if retryHorizon < 24*time.Hour {
		retryHorizon = 24 * time.Hour
	}
	idempotencyExpires := acceptedAt.Add(retryHorizon)
	ingestID := h.ids.NewID("ing")
	maxAttempts := assignment.Endpoint.MaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	err = h.backend.Accept(r.Context(), runtime.AcceptanceRequest{
		Assignment:          assignment,
		LedgerID:            reservation.LedgerID,
		IdempotencyKey:      reservation.IdempotencyKey,
		IngestID:            ingestID,
		PayloadID:           h.ids.NewID("pay"),
		DeliveryID:          h.ids.NewID("del"),
		DispatchJobID:       h.ids.NewID("job"),
		EndpointSnapshotID:  assignment.EndpointSnapshotID,
		Payload:             sealed,
		BodySHA256:          bodyHash,
		Headers:             stable,
		AcceptedAt:          acceptedAt,
		IdempotencyExpires:  idempotencyExpires,
		PayloadExpiresAt:    idempotencyExpires,
		DispatchRunAt:       acceptedAt,
		DispatchMaxAttempts: maxAttempts,
	})
	if errors.Is(err, runtime.ErrUnavailable) || errors.Is(err, runtime.ErrCapacity) {
		writeProblem(w, r, codeIngestUnavailable, requestID, retryAfterHeader(5))
		return
	}
	if err != nil {
		h.serverError(w, r, requestID, "cross acceptance barrier", err)
		return
	}

	h.writeReceipt(w, r, http.StatusAccepted, receiptData{
		ingestID:           ingestID,
		requestID:          requestID,
		stable:             stable,
		bodyHashHex:        bodyHash.String(),
		acceptedAt:         acceptedAt,
		idempotencyExpires: idempotencyExpires,
		duplicate:          false,
	})
}

func (h *Handler) writeDuplicateReceipt(
	w http.ResponseWriter,
	r *http.Request,
	requestID string,
	reservation runtime.Reservation,
	stable runtime.StableHeaders,
	bodyHashHex string,
) {
	acceptedAt := reservation.ReservedAt
	if reservation.AcceptedAt != nil {
		acceptedAt = *reservation.AcceptedAt
	}
	idempotencyExpires := acceptedAt.Add(24 * time.Hour)
	if reservation.IdempotencyExpiresAt != nil {
		idempotencyExpires = *reservation.IdempotencyExpiresAt
	}
	h.writeReceipt(w, r, http.StatusOK, receiptData{
		ingestID:           reservation.IngestID,
		requestID:          requestID,
		stable:             stable,
		bodyHashHex:        bodyHashHex,
		acceptedAt:         acceptedAt.Truncate(time.Second),
		idempotencyExpires: idempotencyExpires.Truncate(time.Second),
		duplicate:          true,
	})
}

func (h *Handler) handleGet(w http.ResponseWriter, r *http.Request) {
	requestID := h.ids.NewID("req")
	now := h.clock.Now()
	ingestID := r.PathValue("ingestID")

	if !h.transportAllowed(r) {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	credentialID, signature, err := ParseAuthorization(r.Header.Get("Authorization"))
	if err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	timestamp, present, duplicate := singleHeader(r.Header, "X-Hookship-Timestamp")
	nonce, noncePresent, nonceDuplicate := singleHeader(r.Header, "X-Hookship-Nonce")
	bodyHashHex, hashPresent, hashDuplicate := singleHeader(r.Header, "X-Hookship-Body-SHA256")
	if !present || duplicate || !noncePresent || nonceDuplicate || !hashPresent || hashDuplicate {
		writeProblem(w, r, codeMalformedRequest, requestID, nil)
		return
	}
	if _, err := ValidateTimestamp(timestamp, now, ReplayWindow); err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}

	// A status GET must carry no body; the signed body hash is SHA-256 of empty.
	empty := make([]byte, 0)
	if body, ok := h.readBody(w, r, requestID, 8); ok {
		empty = body
	} else {
		return
	}
	if len(empty) != 0 || runtime.SHA256(nil).String() != bodyHashHex {
		writeProblem(w, r, codeBodyHashMismatch, requestID, nil)
		return
	}

	assignment, err := h.backend.ResolveCredential(r.Context(), credentialID)
	if err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}
	signed := SignedRequest{
		Method:        http.MethodGet,
		Path:          ingestPath + "/" + ingestID,
		CredentialID:  credentialID,
		Timestamp:     timestamp,
		Nonce:         nonce,
		BodySHA256Hex: bodyHashHex,
	}
	if err := VerifySignature(assignment.HMACSecret, signed, signature); err != nil {
		writeProblem(w, r, codeAuthenticationFailed, requestID, nil)
		return
	}

	status, err := h.backend.Status(r.Context(), assignment.ID, ingestID)
	if errors.Is(err, runtime.ErrNotFound) {
		writeProblem(w, r, codeIngestNotFound, requestID, nil)
		return
	}
	if err != nil {
		h.serverError(w, r, requestID, "read status", err)
		return
	}
	h.writeStatus(w, r, requestID, status)
}

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, requestID, operation string, err error) {
	// Log the operation, never the payload or secret material.
	h.logger.Error("ingest operation failed",
		slog.String("operation", operation),
		slog.String("request_id", requestID),
		slog.String("error", err.Error()),
	)
	writeProblem(w, r, codeIngestUnavailable, requestID, retryAfterHeader(5))
}

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
