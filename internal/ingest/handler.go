package ingest

import (
	"errors"
	"github.com/hookship/hook-service/internal/runtime"
	"log/slog"
	"net/http"
	"regexp"
	"strings"
	"time"
)

const (
	ingestPath   = "/v1/event-ingests"
	nonceRetain  = 600 * time.Second
	maxNonceSize = 512
)

var (
	eventIDPattern   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:-]*$`)
	eventTypePattern = regexp.MustCompile(`^[a-z0-9]+(?:[._-][a-z0-9]+)*$`)
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

func (h *Handler) serverError(w http.ResponseWriter, r *http.Request, requestID, operation string, err error) {
	// Log the operation, never the payload or secret material.
	h.logger.Error("ingest operation failed",
		slog.String("operation", operation),
		slog.String("request_id", requestID),
		slog.String("error", err.Error()),
	)
	writeProblem(w, r, codeIngestUnavailable, requestID, retryAfterHeader(5))
}
