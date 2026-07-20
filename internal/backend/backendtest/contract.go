// Package backendtest provides a shared runtime contract suite that every native
// backend must satisfy. It exercises the acceptance barrier, duplicate and
// conflict handling, delivery job claim/complete/retry/dead lifecycle, fencing,
// the metadata outbox, and payload-free status through the runtime ports only,
// so memory and PostgreSQL profiles are held to identical functional behavior.
package backendtest

import (
	"context"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/runtime"
)

// Factory builds a fresh backend and the assignment it serves. The returned
// cleanup runs at test end.
type Factory func(t *testing.T) (backend runtime.Backend, assignment runtime.Assignment, cleanup func())

// EvaluationAssignment returns the canonical evaluation assignment used by the
// suite, so identifiers and secrets are identical across backends.
func EvaluationAssignment() runtime.Assignment {
	return assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
}

// RunContract executes the full shared contract against a backend factory.
func RunContract(t *testing.T, newBackend Factory) {
	t.Run("AcceptanceAndStatus", func(t *testing.T) { testAcceptance(t, newBackend) })
	t.Run("DuplicateAndConflict", func(t *testing.T) { testDuplicateConflict(t, newBackend) })
	t.Run("DeliveryLifecycle", func(t *testing.T) { testDeliveryLifecycle(t, newBackend) })
	t.Run("FencingRejectsStaleOwner", func(t *testing.T) { testFencing(t, newBackend) })
	t.Run("RetryExhaustionReachesDLQ", func(t *testing.T) { testDLQAndRedrive(t, newBackend) })
	t.Run("NonceReplayProtection", func(t *testing.T) { testNonce(t, newBackend) })
	t.Run("MetadataOutbox", func(t *testing.T) { testOutbox(t, newBackend) })
	t.Run("ForeignAndUnknownStatusIndistinguishable", func(t *testing.T) { testStatusPrivacy(t, newBackend) })
}

func accept(t *testing.T, backend runtime.Backend, assignment runtime.Assignment, body []byte, idempotencyKey string, headers runtime.StableHeaders, now time.Time) (string, string) {
	t.Helper()
	ctx := context.Background()
	bodyHash := runtime.SHA256(body)
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID:       "led_" + idempotencyKey,
		AssignmentID:   assignment.ID,
		IdempotencyKey: idempotencyKey,
		BodySHA256:     bodyHash,
		Headers:        headers,
		ReservedAt:     now,
	})
	if err != nil {
		t.Fatalf("reserve: %v", err)
	}
	if reservation.Status != runtime.ReservationPending {
		t.Fatalf("expected pending, got %s", reservation.Status)
	}
	sealed, err := backend.Seal(body)
	if err != nil {
		t.Fatalf("seal: %v", err)
	}
	ingestID := "ing_" + idempotencyKey
	jobID := "job_" + idempotencyKey
	err = backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment:          assignment,
		LedgerID:            reservation.Reservation.LedgerID,
		IdempotencyKey:      idempotencyKey,
		IngestID:            ingestID,
		PayloadID:           "pay_" + idempotencyKey,
		DeliveryID:          "del_" + idempotencyKey,
		DispatchJobID:       jobID,
		EndpointSnapshotID:  assignment.EndpointSnapshotID,
		Payload:             sealed,
		BodySHA256:          bodyHash,
		Headers:             headers,
		AcceptedAt:          now,
		IdempotencyExpires:  now.Add(24 * time.Hour),
		PayloadExpiresAt:    now.Add(48 * time.Hour),
		DispatchRunAt:       now,
		DispatchMaxAttempts: 3,
	})
	if err != nil {
		t.Fatalf("accept: %v", err)
	}
	return ingestID, jobID
}

func defaultHeaders() runtime.StableHeaders {
	return runtime.StableHeaders{
		EventID:     "evt_001",
		EventType:   "order.created",
		Route:       "orders.primary",
		ContentType: "application/json",
	}
}

func testAcceptance(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	now := time.Now().UTC().Truncate(time.Microsecond)
	ingestID, _ := accept(t, backend, assignment, []byte(`{"order":1}`), "op-accept-1", defaultHeaders(), now)

	status, err := backend.Status(context.Background(), assignment.ID, ingestID)
	if err != nil {
		t.Fatalf("status: %v", err)
	}
	if status.IngestID != ingestID {
		t.Fatalf("status ingest id = %q", status.IngestID)
	}
	if status.Headers.EventType != "order.created" {
		t.Fatalf("status event type = %q", status.Headers.EventType)
	}
	if status.DispatchState != "queued" {
		t.Fatalf("dispatch state = %q", status.DispatchState)
	}
}

func testDuplicateConflict(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	body := []byte(`{"order":2}`)
	accept(t, backend, assignment, body, "op-dup-1", defaultHeaders(), now)

	// Duplicate: same body + headers on a committed key.
	duplicate, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID:       "led_dup_again",
		AssignmentID:   assignment.ID,
		IdempotencyKey: "op-dup-1",
		BodySHA256:     runtime.SHA256(body),
		Headers:        defaultHeaders(),
		ReservedAt:     now.Add(time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.Status != runtime.ReservationDuplicate {
		t.Fatalf("expected duplicate, got %s", duplicate.Status)
	}

	// Body conflict.
	conflict, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID:       "led_conflict",
		AssignmentID:   assignment.ID,
		IdempotencyKey: "op-dup-1",
		BodySHA256:     runtime.SHA256([]byte(`{"order":999}`)),
		Headers:        defaultHeaders(),
		ReservedAt:     now.Add(2 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if conflict.Status != runtime.ReservationConflict {
		t.Fatalf("expected conflict, got %s", conflict.Status)
	}

	// Metadata conflict: same body, changed stable header.
	changed := defaultHeaders()
	changed.Route = "orders.secondary"
	metadata, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID:       "led_meta",
		AssignmentID:   assignment.ID,
		IdempotencyKey: "op-dup-1",
		BodySHA256:     runtime.SHA256(body),
		Headers:        changed,
		ReservedAt:     now.Add(3 * time.Second),
	})
	if err != nil {
		t.Fatal(err)
	}
	if metadata.Status != runtime.ReservationMetadataConflict {
		t.Fatalf("expected metadata conflict, got %s", metadata.Status)
	}
}

func testDeliveryLifecycle(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	body := []byte(`{"order":3}`)
	_, jobID := accept(t, backend, assignment, body, "op-deliver-1", defaultHeaders(), now)

	job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if job.JobID != jobID {
		t.Fatalf("claimed unexpected job %q", job.JobID)
	}
	if job.Attempts != 1 {
		t.Fatalf("attempts = %d", job.Attempts)
	}

	delivery, err := backend.LoadDelivery(ctx, job)
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	if string(delivery.Body) != string(body) {
		t.Fatalf("decrypted body mismatch: %q", delivery.Body)
	}
	if delivery.Endpoint.URL != assignment.Endpoint.URL {
		t.Fatalf("endpoint url = %q", delivery.Endpoint.URL)
	}

	lease := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "token-1", Attempt: job.Attempts}
	if err := backend.CompleteJob(ctx, lease); err != nil {
		t.Fatalf("complete: %v", err)
	}
	if _, ok, _ := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token-2", 30*time.Second); ok {
		t.Fatal("completed job should not be claimable again")
	}
}

func testFencing(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	accept(t, backend, assignment, []byte(`{"order":4}`), "op-fence-1", defaultHeaders(), now)

	job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token-1", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: %v", err)
	}
	// A stale owner (wrong token) must not settle the job.
	stale := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-b", Token: "wrong", Attempt: job.Attempts}
	if err := backend.CompleteJob(ctx, stale); err != runtime.ErrLeaseLost {
		t.Fatalf("expected ErrLeaseLost for stale owner, got %v", err)
	}
	// The rightful owner can still settle.
	good := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "token-1", Attempt: job.Attempts}
	if err := backend.CompleteJob(ctx, good); err != nil {
		t.Fatalf("rightful owner complete: %v", err)
	}
}

func testDLQAndRedrive(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	accept(t, backend, assignment, []byte(`{"order":5}`), "op-dlq-1", defaultHeaders(), now)

	// Exhaust the three attempts with immediate retries.
	for attempt := 1; attempt <= 3; attempt++ {
		job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("claim attempt %d: ok=%v err=%v", attempt, ok, err)
		}
		lease := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "token", Attempt: job.Attempts}
		if err := backend.RetryJob(ctx, lease, 0, "retryable_5xx"); err != nil {
			t.Fatalf("retry attempt %d: %v", attempt, err)
		}
	}

	dead, err := backend.ListDeadJobs(ctx, assignment.ID, 10)
	if err != nil {
		t.Fatalf("list dead: %v", err)
	}
	if len(dead) != 1 {
		t.Fatalf("expected one dead job, got %d", len(dead))
	}

	if err := backend.RedriveJob(ctx, assignment.ID, dead[0].JobID, now); err != nil {
		t.Fatalf("redrive: %v", err)
	}
	if _, ok, _ := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token", 30*time.Second); !ok {
		t.Fatal("redriven job should be claimable")
	}
}

func testNonce(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC()
	fresh, err := backend.ReserveNonce(ctx, assignment.CredentialID, "nonce-abc", now, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if !fresh {
		t.Fatal("first nonce use should be fresh")
	}
	replay, err := backend.ReserveNonce(ctx, assignment.CredentialID, "nonce-abc", now, now.Add(10*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if replay {
		t.Fatal("nonce replay must be rejected")
	}
}

func testOutbox(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	_, jobID := accept(t, backend, assignment, []byte(`{"order":6}`), "op-outbox-1", defaultHeaders(), now)

	job, _, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "token", 30*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	status := 200
	attempt := runtime.AttemptRecord{
		AttemptID:          "att_1",
		AssignmentID:       assignment.ID,
		DeliveryID:         "del_op-outbox-1",
		JobID:              jobID,
		EndpointSnapshotID: assignment.EndpointSnapshotID,
		AttemptNumber:      job.Attempts,
		Outcome:            runtime.OutcomeSuccess,
		ResponseStatus:     &status,
		DurationMS:         12,
		StartedAt:          now,
		FinishedAt:         now.Add(12 * time.Millisecond),
	}
	observation := runtime.OutboxObservation{
		OutboxID:           "obx_1",
		AssignmentID:       assignment.ID,
		DeliveryID:         "del_op-outbox-1",
		AttemptID:          "att_1",
		EndpointSnapshotID: assignment.EndpointSnapshotID,
		SchemaVersion:      "1",
		EventKind:          "delivery.attempted",
		Outcome:            runtime.OutcomeSuccess,
		ResponseStatus:     &status,
		OccurredAt:         now,
		RunAt:              now,
		MaxAttempts:        5,
	}
	if err := backend.RecordAttempt(ctx, attempt, observation); err != nil {
		t.Fatalf("record attempt: %v", err)
	}

	item, ok, err := backend.ClaimOutbox(ctx, assignment.ID, "worker-a", "otoken", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim outbox: ok=%v err=%v", ok, err)
	}
	lease := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "worker-a", Token: "otoken", Attempt: item.Attempts}
	if err := backend.PublishOutbox(ctx, lease); err != nil {
		t.Fatalf("publish outbox: %v", err)
	}
}

func testStatusPrivacy(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	// Unknown ID and a foreign assignment ID must both return ErrNotFound.
	if _, err := backend.Status(ctx, assignment.ID, "ing_does_not_exist"); err != runtime.ErrNotFound {
		t.Fatalf("expected ErrNotFound for unknown id, got %v", err)
	}
	now := time.Now().UTC().Truncate(time.Microsecond)
	ingestID, _ := accept(t, backend, assignment, []byte(`{"order":7}`), "op-priv-1", defaultHeaders(), now)
	if _, err := backend.Status(ctx, "asg_other_tenant", ingestID); err != runtime.ErrNotFound {
		t.Fatalf("expected ErrNotFound for foreign assignment, got %v", err)
	}
}
