package backendtest

import (
	"context"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// enqueueOutbox drives one accepted delivery through a claimed job and a
// recorded attempt so a single metadata-outbox row exists, then returns its ID.
// maxAttempts bounds the outbox row so exhaustion can be exercised cheaply.
func enqueueOutbox(t *testing.T, backend runtime.Backend, assignment runtime.Assignment, opKey string, now time.Time, maxAttempts int) string {
	t.Helper()
	ctx := context.Background()
	_, jobID := accept(t, backend, assignment, []byte(`{"o":1}`), opKey, defaultHeaders(), now)
	job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "wq-"+opKey, "tq-"+opKey, 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("enqueueOutbox claim job: ok=%v err=%v", ok, err)
	}
	status := 200
	attempt := runtime.AttemptRecord{
		AttemptID:          "att_" + opKey,
		AssignmentID:       assignment.ID,
		DeliveryID:         "del_" + opKey,
		JobID:              jobID,
		EndpointSnapshotID: assignment.EndpointSnapshotID,
		AttemptNumber:      job.Attempts,
		Outcome:            runtime.OutcomeSuccess,
		ResponseStatus:     &status,
		DurationMS:         5,
		StartedAt:          now,
		FinishedAt:         now.Add(5 * time.Millisecond),
	}
	observation := runtime.OutboxObservation{
		OutboxID:           "obx_" + opKey,
		AssignmentID:       assignment.ID,
		DeliveryID:         "del_" + opKey,
		AttemptID:          "att_" + opKey,
		EndpointSnapshotID: assignment.EndpointSnapshotID,
		SchemaVersion:      "1",
		EventKind:          "delivery.attempted",
		Outcome:            runtime.OutcomeSuccess,
		ResponseStatus:     &status,
		OccurredAt:         now,
		RunAt:              now,
		MaxAttempts:        maxAttempts,
	}
	if err := backend.RecordAttempt(ctx, attempt, observation); err != nil {
		t.Fatalf("enqueueOutbox record attempt: %v", err)
	}
	return "obx_" + opKey
}

// testLeaseExpiryReclaim proves that an expired lease is reclaimable by a new
// owner on both the job and the outbox queue, that the reclaim advances the
// attempt, and that the original owner can no longer settle (fenced by expiry).
// It uses a tiny real lease and a generous sleep, so it is deterministic against
// both the in-memory clock and the PostgreSQL database clock.
func testLeaseExpiryReclaim(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	// Job queue.
	accept(t, backend, assignment, []byte(`{"o":1}`), "op-jexp", defaultHeaders(), now)
	job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "tok-a", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("initial job claim: ok=%v err=%v", ok, err)
	}
	stale := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "tok-a", Attempt: job.Attempts}
	time.Sleep(60 * time.Millisecond)
	reclaimed, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-b", "tok-b", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("expired job lease must be reclaimable: ok=%v err=%v", ok, err)
	}
	if reclaimed.JobID != job.JobID {
		t.Fatalf("reclaim should return the same job, got %s want %s", reclaimed.JobID, job.JobID)
	}
	if reclaimed.Attempts <= job.Attempts {
		t.Fatalf("reclaim must advance the attempt, got %d after %d", reclaimed.Attempts, job.Attempts)
	}
	if err := backend.CompleteJob(ctx, stale); err != runtime.ErrLeaseLost {
		t.Fatalf("stale owner settle after expiry must be ErrLeaseLost, got %v", err)
	}
	good := runtime.JobLease{AssignmentID: assignment.ID, JobID: reclaimed.JobID, Owner: "worker-b", Token: "tok-b", Attempt: reclaimed.Attempts}
	if err := backend.CompleteJob(ctx, good); err != nil {
		t.Fatalf("new owner settle after reclaim: %v", err)
	}

	// Outbox queue.
	enqueueOutbox(t, backend, assignment, "op-oexp", now, 5)
	item, ok, err := backend.ClaimOutbox(ctx, assignment.ID, "ob-a", "otk-a", time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("initial outbox claim: ok=%v err=%v", ok, err)
	}
	staleOutbox := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "ob-a", Token: "otk-a", Attempt: item.Attempts}
	time.Sleep(60 * time.Millisecond)
	reItem, ok, err := backend.ClaimOutbox(ctx, assignment.ID, "ob-b", "otk-b", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("expired outbox lease must be reclaimable: ok=%v err=%v", ok, err)
	}
	if reItem.Attempts <= item.Attempts {
		t.Fatalf("outbox reclaim must advance the attempt, got %d after %d", reItem.Attempts, item.Attempts)
	}
	if err := backend.PublishOutbox(ctx, staleOutbox); err != runtime.ErrLeaseLost {
		t.Fatalf("stale outbox settle after expiry must be ErrLeaseLost, got %v", err)
	}
}

// testStaleFenceRejection proves the fence predicate rejects a wrong token and a
// wrong attempt (not just a wrong owner) on both queues, and accepts the exact
// held lease.
func testStaleFenceRejection(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	accept(t, backend, assignment, []byte(`{"o":2}`), "op-jfence", defaultHeaders(), now)
	job, ok, err := backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, "worker-a", "tok-a", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim job: ok=%v err=%v", ok, err)
	}
	wrongToken := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "tok-wrong", Attempt: job.Attempts}
	if err := backend.CompleteJob(ctx, wrongToken); err != runtime.ErrLeaseLost {
		t.Fatalf("wrong token must be ErrLeaseLost, got %v", err)
	}
	wrongAttempt := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "tok-a", Attempt: job.Attempts + 7}
	if err := backend.CompleteJob(ctx, wrongAttempt); err != runtime.ErrLeaseLost {
		t.Fatalf("wrong attempt must be ErrLeaseLost, got %v", err)
	}
	held := runtime.JobLease{AssignmentID: assignment.ID, JobID: job.JobID, Owner: "worker-a", Token: "tok-a", Attempt: job.Attempts}
	if err := backend.CompleteJob(ctx, held); err != nil {
		t.Fatalf("exact held lease must settle: %v", err)
	}

	enqueueOutbox(t, backend, assignment, "op-ofence", now, 5)
	item, ok, err := backend.ClaimOutbox(ctx, assignment.ID, "ob-a", "otk-a", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim outbox: ok=%v err=%v", ok, err)
	}
	wrongOutboxToken := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "ob-a", Token: "otk-wrong", Attempt: item.Attempts}
	if err := backend.PublishOutbox(ctx, wrongOutboxToken); err != runtime.ErrLeaseLost {
		t.Fatalf("wrong outbox token must be ErrLeaseLost, got %v", err)
	}
	wrongOutboxAttempt := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "ob-a", Token: "otk-a", Attempt: item.Attempts + 7}
	if err := backend.PublishOutbox(ctx, wrongOutboxAttempt); err != runtime.ErrLeaseLost {
		t.Fatalf("wrong outbox attempt must be ErrLeaseLost, got %v", err)
	}
	heldOutbox := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "ob-a", Token: "otk-a", Attempt: item.Attempts}
	if err := backend.PublishOutbox(ctx, heldOutbox); err != nil {
		t.Fatalf("exact held outbox lease must settle: %v", err)
	}
}

// testOutboxFencingAndExhaustion proves the outbox queue reaches its exhaustion
// boundary: after max_attempts retries it becomes dead-lettered and is no longer
// claimable, mirroring the job-queue DLQ guarantee.
func testOutboxFencingAndExhaustion(t *testing.T, newBackend Factory) {
	backend, assignment, cleanup := newBackend(t)
	defer cleanup()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)

	const maxAttempts = 2
	enqueueOutbox(t, backend, assignment, "op-oexhaust", now, maxAttempts)

	for attempt := 1; attempt <= maxAttempts; attempt++ {
		item, ok, err := backend.ClaimOutbox(ctx, assignment.ID, "ob-a", "otk", 30*time.Second)
		if err != nil || !ok {
			t.Fatalf("outbox claim %d: ok=%v err=%v", attempt, ok, err)
		}
		lease := runtime.OutboxLease{AssignmentID: assignment.ID, OutboxID: item.OutboxID, Owner: "ob-a", Token: "otk", Attempt: item.Attempts}
		if err := backend.RetryOutbox(ctx, lease, 0, "sink_unavailable"); err != nil {
			t.Fatalf("outbox retry %d: %v", attempt, err)
		}
	}

	if _, ok, _ := backend.ClaimOutbox(ctx, assignment.ID, "ob-a", "otk", 30*time.Second); ok {
		t.Fatal("exhausted outbox row must not be claimable")
	}
}
