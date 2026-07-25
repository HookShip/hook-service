package ingest

import (
	"errors"
	"github.com/hookship/hook-service/internal/runtime"
	"net/http"
	"time"
)

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
