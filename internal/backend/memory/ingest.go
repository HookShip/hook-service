package memory

import (
	"bytes"
	"context"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// ReserveIdempotency reserves or resolves an idempotency identity. The semantics
// mirror the durable ledger: a first reservation is pending, a matching retry of
// a committed entry is a duplicate, and body/metadata mismatches or tombstones
// are surfaced as conflicts.
func (b *Backend) ReserveIdempotency(_ context.Context, request runtime.ReservationRequest) (runtime.ReservationResult, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock.Now()
	b.evictExpiredLocked(now)

	key := ledgerKey(request.AssignmentID, request.IdempotencyKey)
	fingerprint := runtime.RequestFingerprint(request.BodySHA256, request.Headers)

	entry, exists := b.ledgers[key]
	if !exists {
		if len(b.ledgers) >= b.limits.MaxLedgers {
			return runtime.ReservationResult{}, runtime.ErrCapacity
		}
		reservedAt := request.ReservedAt
		if reservedAt.IsZero() {
			reservedAt = now
		}
		entry = &ledgerEntry{
			ledgerID:       request.LedgerID,
			assignmentID:   request.AssignmentID,
			idempotencyKey: request.IdempotencyKey,
			fingerprint:    fingerprint,
			bodySHA256:     request.BodySHA256,
			headers:        request.Headers,
			state:          "pending",
			reservedAt:     reservedAt,
		}
		b.ledgers[key] = entry
		return runtime.ReservationResult{
			Status:      runtime.ReservationPending,
			Created:     true,
			Reservation: entry.toReservation(),
		}, nil
	}

	result := runtime.ReservationResult{Created: false, Reservation: entry.toReservation()}
	switch {
	case entry.state == "tombstone":
		result.Status = runtime.ReservationExpired
	case !bytes.Equal(entry.bodySHA256[:], request.BodySHA256[:]):
		result.Status = runtime.ReservationConflict
	case entry.headers != request.Headers || !bytes.Equal(entry.fingerprint[:], fingerprint[:]):
		result.Status = runtime.ReservationMetadataConflict
	case entry.state == "committed":
		result.Status = runtime.ReservationDuplicate
	default:
		result.Status = runtime.ReservationPending
	}
	return result, nil
}

// Accept crosses the in-memory acceptance barrier atomically under the backend
// lock: ingest, payload, dispatch job, and ledger commit all become visible
// together or not at all. This is not durable; a restart loses everything.
func (b *Backend) Accept(_ context.Context, request runtime.AcceptanceRequest) error {
	if int64(len(request.Payload.Ciphertext)) > b.limits.MaxPayloadBytes {
		return runtime.ErrCapacity
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	now := b.clock.Now()
	b.evictExpiredLocked(now)

	key := ledgerKey(request.Assignment.ID, request.IdempotencyKey)
	entry, ok := b.ledgers[key]
	if !ok || entry.state != "pending" {
		// The reservation vanished (eviction) or already advanced; the caller
		// must retry rather than receive a false acceptance.
		return runtime.ErrUnavailable
	}
	if _, ok := b.snapshots[request.EndpointSnapshotID]; !ok {
		return runtime.ErrNotFound
	}
	if len(b.jobs) >= b.limits.MaxJobs {
		return runtime.ErrCapacity
	}

	acceptedAt := request.AcceptedAt
	if acceptedAt.IsZero() {
		acceptedAt = now
	}
	expiresAt := request.IdempotencyExpires
	if expiresAt.IsZero() {
		expiresAt = acceptedAt.Add(24 * time.Hour)
	}
	payloadExpiry := request.PayloadExpiresAt
	if payloadExpiry.IsZero() {
		payloadExpiry = acceptedAt.Add(b.limits.TTL)
	}
	runAt := request.DispatchRunAt
	if runAt.IsZero() {
		runAt = acceptedAt
	}
	maxAttempts := request.DispatchMaxAttempts
	if maxAttempts < 1 {
		maxAttempts = 1
	}

	b.payloads[request.PayloadID] = &payloadRecord{
		assignmentID: request.Assignment.ID,
		sealed:       request.Payload,
		expiresAt:    payloadExpiry,
	}
	b.ingests[request.IngestID] = &ingestRecord{
		assignmentID: request.Assignment.ID,
		headers:      request.Headers,
		bodySHA256:   request.BodySHA256,
		acceptedAt:   acceptedAt,
		expiresAt:    expiresAt,
		updatedAt:    acceptedAt,
		payloadID:    request.PayloadID,
		dispatchJob:  request.DispatchJobID,
	}
	b.jobs[request.DispatchJobID] = &jobEntry{
		job: runtime.Job{
			JobID:              request.DispatchJobID,
			AssignmentID:       request.Assignment.ID,
			Kind:               runtime.JobKindDispatch,
			IngestID:           request.IngestID,
			PayloadID:          request.PayloadID,
			DeliveryID:         request.DeliveryID,
			EndpointSnapshotID: request.EndpointSnapshotID,
			State:              "ready",
			RunAt:              runAt,
			MaxAttempts:        maxAttempts,
		},
		createdAt: acceptedAt,
	}

	entry.state = "committed"
	entry.ingestID = request.IngestID
	entry.payloadID = request.PayloadID
	entry.dispatchJobID = request.DispatchJobID
	entry.acceptedAt = acceptedAt
	entry.expiresAt = expiresAt
	return nil
}

// Status returns the payload-free status of an ingest owned by the assignment.
// Unknown and foreign IDs are indistinguishable (ErrNotFound).
func (b *Backend) Status(_ context.Context, assignmentID, ingestID string) (runtime.IngestStatus, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	record, ok := b.ingests[ingestID]
	if !ok || record.assignmentID != assignmentID {
		return runtime.IngestStatus{}, runtime.ErrNotFound
	}
	dispatchState := "unknown"
	if job, ok := b.jobs[record.dispatchJob]; ok {
		dispatchState = mapJobStateToDispatch(job.job.State)
	}
	return runtime.IngestStatus{
		IngestID:             ingestID,
		AssignmentID:         assignmentID,
		Headers:              record.headers,
		BodySHA256:           record.bodySHA256,
		AcceptedAt:           record.acceptedAt,
		IdempotencyExpiresAt: record.expiresAt,
		UpdatedAt:            record.updatedAt,
		IngestState:          "ephemeral",
		IdempotencyState:     "committed",
		DispatchState:        dispatchState,
	}, nil
}

func mapJobStateToDispatch(state string) string {
	switch state {
	case "ready", "retry":
		return "queued"
	case "leased":
		return "claimed"
	case "succeeded":
		return "completed"
	case "dead":
		return "failed"
	default:
		return "queued"
	}
}

func (e *ledgerEntry) toReservation() runtime.Reservation {
	reservation := runtime.Reservation{
		LedgerID:       e.ledgerID,
		AssignmentID:   e.assignmentID,
		IdempotencyKey: e.idempotencyKey,
		BodySHA256:     e.bodySHA256,
		Headers:        e.headers,
		State:          e.state,
		IngestID:       e.ingestID,
		PayloadID:      e.payloadID,
		DispatchJobID:  e.dispatchJobID,
		ReservedAt:     e.reservedAt,
	}
	if !e.acceptedAt.IsZero() {
		acceptedAt := e.acceptedAt
		reservation.AcceptedAt = &acceptedAt
	}
	if !e.expiresAt.IsZero() {
		expiresAt := e.expiresAt
		reservation.IdempotencyExpiresAt = &expiresAt
	}
	return reservation
}

// evictExpiredLocked removes ephemeral entries past their TTL. Because the
// memory backend is evaluation-only, aggressive eviction is acceptable and
// proves that state does not persist.
func (b *Backend) evictExpiredLocked(now time.Time) {
	b.evictNoncesLocked(now)
	for key, entry := range b.ledgers {
		if entry.state == "committed" && !entry.expiresAt.IsZero() && !entry.expiresAt.After(now) {
			entry.state = "tombstone"
		}
		if entry.state != "committed" && !entry.reservedAt.IsZero() && now.Sub(entry.reservedAt) > 2*b.limits.TTL {
			delete(b.ledgers, key)
		}
	}
	for id, payload := range b.payloads {
		if !payload.expiresAt.IsZero() && !payload.expiresAt.After(now) {
			delete(b.payloads, id)
		}
	}
}
