//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend/objectstore"
	backendpg "github.com/hookship/hook-service/internal/backend/postgres"
	durablepg "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// retryStageLease is a short stage-write lease so a "crashed" writer's fence is
// reclaimed quickly and a retry can safely supersede within the test.
const retryStageLease = 250 * time.Millisecond

// objectRetryHarness bundles a durable backend with the in-memory object store
// (which enforces immutability and checksums like the real S3 client) so a
// retry after a simulated failure or crash can be driven deterministically.
type objectRetryHarness struct {
	pool     *pgxpool.Pool
	backend  *backendpg.Backend
	store    *objectstore.MemoryStore
	repo     *durablepg.Repository
	resolved runtime.Assignment
}

func newObjectRetryHarness(t *testing.T) (*objectRetryHarness, func()) {
	t.Helper()
	pool, cleanup := newIsolatedPool(t)
	store := objectstore.NewMemoryStore()
	repo, err := durablepg.New(pool)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
	backend, err := backendpg.New(context.Background(), pool, []runtime.Assignment{base}, newTestSealer(t), runtime.SystemClock{}, false,
		backendpg.WithPayloadStore(store),
		backendpg.WithStageWriteLease(retryStageLease))
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	resolved, err := backend.ResolveCredential(context.Background(), base.CredentialID)
	if err != nil {
		cleanup()
		t.Fatal(err)
	}
	h := &objectRetryHarness{pool: pool, backend: backend, store: store, repo: repo, resolved: resolved}
	return h, func() {
		backend.Close()
		cleanup()
	}
}

func objectHeaders(key string) runtime.StableHeaders {
	return runtime.StableHeaders{EventID: "evt_" + key, EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
}

// acceptOnce performs a single acceptance attempt with a fresh random-nonce seal
// under a stable idempotency key/ledger/payload, generating a fresh ingest
// lineage each call. It returns (ingestID, err).
func (h *objectRetryHarness) acceptOnce(ctx context.Context, ledgerID, idemKey, payloadID string, body []byte, attempt int) (string, error) {
	sealed, err := h.backend.Seal(body)
	if err != nil {
		return "", err
	}
	ingestID := "ing_" + idemKey + "_" + strconv.Itoa(attempt)
	now := runtime.SystemClock{}.Now()
	return ingestID, h.backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: h.resolved, LedgerID: ledgerID, IdempotencyKey: idemKey,
		IngestID: ingestID, PayloadID: payloadID, DeliveryID: "del_" + ingestID, DispatchJobID: "job_" + ingestID,
		EndpointSnapshotID: h.resolved.EndpointSnapshotID, Payload: sealed, BodySHA256: runtime.SHA256(body), Headers: objectHeaders(idemKey),
		AcceptedAt: now, IdempotencyExpires: now.Add(25 * time.Hour), PayloadExpiresAt: now.Add(48 * time.Hour),
		DispatchRunAt: now, DispatchMaxAttempts: 5,
	})
}

// retryFreshUntil202 retries acceptance with a fresh random-nonce re-seal until
// it commits (nil) or the deadline elapses. Every failure before success must be
// retryable unavailability, never a false acceptance. Returns the committed
// ingest ID.
func (h *objectRetryHarness) retryFreshUntil202(t *testing.T, ctx context.Context, ledgerID, idemKey, payloadID string, body []byte) string {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for attempt := 1; ; attempt++ {
		ingestID, err := h.acceptOnce(ctx, ledgerID, idemKey, payloadID, body, attempt)
		if err == nil {
			return ingestID
		}
		if !errors.Is(err, runtime.ErrUnavailable) {
			t.Fatalf("retry produced a non-retryable error (possible false acceptance risk): %v", err)
		}
		// No commit may be observable while acceptance is still failing.
		if _, statusErr := h.backend.Status(ctx, h.resolved.ID, ingestID); !errors.Is(statusErr, runtime.ErrNotFound) {
			t.Fatalf("failed acceptance became observable as accepted: %v", statusErr)
		}
		if time.Now().After(deadline) {
			t.Fatalf("retry under idempotency key %q never reached 202", idemKey)
		}
		time.Sleep(40 * time.Millisecond)
	}
}

// assertDeliverable claims the committed dispatch job and confirms the object
// round-trips back to the original plaintext, proving object-backed delivery.
func (h *objectRetryHarness) assertDeliverable(t *testing.T, ctx context.Context, body []byte) runtime.Job {
	t.Helper()
	job, ok, err := h.backend.ClaimJob(ctx, h.resolved.ID, runtime.JobKindDispatch, "worker", "wtok", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim committed job: ok=%v err=%v", ok, err)
	}
	delivery, err := h.backend.LoadDelivery(ctx, job)
	if err != nil {
		t.Fatalf("load delivery: %v", err)
	}
	if string(delivery.Body) != string(body) {
		t.Fatalf("delivered body mismatch: %q", delivery.Body)
	}
	return job
}

func (h *objectRetryHarness) reserve(t *testing.T, ctx context.Context, ledgerID, idemKey string, body []byte) {
	t.Helper()
	if _, err := h.backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: ledgerID, AssignmentID: h.resolved.ID, IdempotencyKey: idemKey,
		BodySHA256: runtime.SHA256(body), Headers: objectHeaders(idemKey),
	}); err != nil {
		t.Fatal(err)
	}
}

// countSupersededAttempts returns how many stage-write attempts exist for a
// payload; a supersession retains the prior (orphaned) attempt alongside the
// promoted one, so a value >= 2 proves superseded ciphertext is retained for
// cleanup.
func (h *objectRetryHarness) attemptStates(t *testing.T, ctx context.Context, payloadID string) []string {
	t.Helper()
	records, err := h.repo.ListObjectStageCleanup(ctx, h.resolved.ID, payloadID)
	if err != nil {
		t.Fatal(err)
	}
	states := make([]string, 0, len(records))
	for _, r := range records {
		states = append(states, r.State)
	}
	return states
}

// TestObjectRetryCrashBeforePut covers a crash before the staging object is
// written: the first attempt claims and faults on the staging PUT. A retry with
// a fresh random seal must supersede the stale identity (after the fence
// expires) and reach 202, then deliver.
func TestObjectRetryCrashBeforePut(t *testing.T) {
	h, cleanup := newObjectRetryHarness(t)
	defer cleanup()
	ctx := context.Background()
	// Fail only the first attempt's staging PUT; the superseding attempt's keys
	// (attempt 2+) succeed.
	h.store.SetPutFault(func(key string) error {
		if strings.Contains(key, "/attempts/0000000001/staging/") {
			return errors.New("simulated crash before staging PUT")
		}
		return nil
	})

	body := []byte(`{"crash":"before-put"}`)
	h.reserve(t, ctx, "led_crash1", "op-crash1", body)

	// First attempt fails closed (no false acceptance).
	if _, err := h.acceptOnce(ctx, "led_crash1", "op-crash1", "pay_crash1", body, 0); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("first attempt must fail closed, got %v", err)
	}
	// Retry with fresh seals until it commits.
	committed := h.retryFreshUntil202(t, ctx, "led_crash1", "op-crash1", "pay_crash1", body)
	if status, err := h.backend.Status(ctx, h.resolved.ID, committed); err != nil || status.DispatchState != "queued" {
		t.Fatalf("committed status: state=%q err=%v", status.DispatchState, err)
	}
	if states := h.attemptStates(t, ctx, "pay_crash1"); len(states) < 2 {
		t.Fatalf("expected a superseded attempt retained for cleanup, got %v", states)
	}
	h.assertDeliverable(t, ctx, body)
}

// TestObjectRetryCrashAfterStagingPut covers a crash after the staging object is
// written but before the final object: the first attempt writes staging then
// faults on the final PUT, leaving an orphaned staging object.
func TestObjectRetryCrashAfterStagingPut(t *testing.T) {
	h, cleanup := newObjectRetryHarness(t)
	defer cleanup()
	ctx := context.Background()
	h.store.SetPutFault(func(key string) error {
		if strings.Contains(key, "/attempts/0000000001/final/") {
			return errors.New("simulated crash after staging PUT")
		}
		return nil
	})

	body := []byte(`{"crash":"after-staging"}`)
	h.reserve(t, ctx, "led_crash2", "op-crash2", body)
	if _, err := h.acceptOnce(ctx, "led_crash2", "op-crash2", "pay_crash2", body, 0); !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("first attempt must fail closed, got %v", err)
	}
	if h.store.ObjectCount() == 0 {
		t.Fatal("expected a staging object written before the simulated crash")
	}
	h.retryFreshUntil202(t, ctx, "led_crash2", "op-crash2", "pay_crash2", body)
	if states := h.attemptStates(t, ctx, "pay_crash2"); len(states) < 2 {
		t.Fatalf("expected a superseded attempt retained for cleanup, got %v", states)
	}
	h.assertDeliverable(t, ctx, body)
}

// TestObjectRetryCrashAfterFinalPutBeforeFinalize simulates a crash after both
// objects were written but before FinalizeObjectPayload: the payload is still
// `staging` with a leased attempt. A retry with a fresh seal must supersede once
// the fence expires and reach 202.
func TestObjectRetryCrashAfterFinalPutBeforeFinalize(t *testing.T) {
	h, cleanup := newObjectRetryHarness(t)
	defer cleanup()
	ctx := context.Background()

	body := []byte(`{"crash":"after-final-put"}`)
	reservation, err := h.backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_crash3", AssignmentID: h.resolved.ID, IdempotencyKey: "op-crash3",
		BodySHA256: runtime.SHA256(body), Headers: objectHeaders("op-crash3"),
	})
	if err != nil {
		t.Fatal(err)
	}
	// Manual first attempt: stage, claim, PUT both objects, then "crash" without
	// finalizing.
	sealed1, _ := h.backend.Seal(body)
	now := runtime.SystemClock{}.Now()
	if err := h.repo.StageObjectPayload(ctx, durablepg.ObjectPayloadStage{
		AssignmentID: h.resolved.ID, LedgerID: reservation.Reservation.LedgerID, PayloadID: "pay_crash3",
		KeyReference: sealed1.KeyReference, KeyVersion: sealed1.KeyVersion, Nonce: sealed1.Nonce,
		CiphertextSHA256: durablepg.Hash(sealed1.CiphertextSHA256), CiphertextSize: int64(len(sealed1.Ciphertext)),
		ExpiresAt: now.Add(48 * time.Hour), StagedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := h.repo.ClaimObjectStageWrite(ctx, h.resolved.ID, reservation.Reservation.LedgerID, "crashed", "crashed-token", retryStageLease)
	if err != nil || !ok {
		t.Fatalf("manual claim: ok=%v err=%v", ok, err)
	}
	checksum := runtime.Hash(lease.CiphertextSHA256)
	if _, err := h.store.PutImmutable(ctx, lease.StagingObjectKey, sealed1.Ciphertext, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutImmutable(ctx, lease.FinalObjectKey, sealed1.Ciphertext, checksum); err != nil {
		t.Fatal(err)
	}
	// Crash: do not finalize.

	h.retryFreshUntil202(t, ctx, reservation.Reservation.LedgerID, "op-crash3", "pay_crash3", body)
	if states := h.attemptStates(t, ctx, "pay_crash3"); len(states) < 2 {
		t.Fatalf("expected a superseded attempt retained for cleanup, got %v", states)
	}
	// Both superseded objects and the new attempt's objects remain until cleanup.
	h.assertDeliverable(t, ctx, body)
}

// TestObjectRetryCrashAfterFinalizeBeforeAccept simulates a crash after the
// payload is finalized but before AcceptFinalizedObject: the durable object
// already exists. A retry must resume it (never rebind) and reach 202
// immediately, delivering the finalized object rather than the retry's re-seal.
func TestObjectRetryCrashAfterFinalizeBeforeAccept(t *testing.T) {
	h, cleanup := newObjectRetryHarness(t)
	defer cleanup()
	ctx := context.Background()

	body := []byte(`{"crash":"after-finalize"}`)
	reservation, err := h.backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_crash4", AssignmentID: h.resolved.ID, IdempotencyKey: "op-crash4",
		BodySHA256: runtime.SHA256(body), Headers: objectHeaders("op-crash4"),
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed1, _ := h.backend.Seal(body)
	now := runtime.SystemClock{}.Now()
	if err := h.repo.StageObjectPayload(ctx, durablepg.ObjectPayloadStage{
		AssignmentID: h.resolved.ID, LedgerID: reservation.Reservation.LedgerID, PayloadID: "pay_crash4",
		KeyReference: sealed1.KeyReference, KeyVersion: sealed1.KeyVersion, Nonce: sealed1.Nonce,
		CiphertextSHA256: durablepg.Hash(sealed1.CiphertextSHA256), CiphertextSize: int64(len(sealed1.Ciphertext)),
		ExpiresAt: now.Add(48 * time.Hour), StagedAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	lease, ok, err := h.repo.ClaimObjectStageWrite(ctx, h.resolved.ID, reservation.Reservation.LedgerID, "crashed", "crashed-token", time.Hour)
	if err != nil || !ok {
		t.Fatalf("manual claim: ok=%v err=%v", ok, err)
	}
	checksum := runtime.Hash(lease.CiphertextSHA256)
	if _, err := h.store.PutImmutable(ctx, lease.StagingObjectKey, sealed1.Ciphertext, checksum); err != nil {
		t.Fatal(err)
	}
	if _, err := h.store.PutImmutable(ctx, lease.FinalObjectKey, sealed1.Ciphertext, checksum); err != nil {
		t.Fatal(err)
	}
	if err := h.repo.FinalizeObjectPayload(ctx, lease, durablepg.ObjectPromotion{
		ProviderStagingVersion: "stage-v1", ProviderFinalVersion: "final-v1", Mode: "create_only",
	}); err != nil {
		t.Fatal(err)
	}
	// Crash: do not AcceptFinalizedObject.

	// The retry resumes the finalized object immediately (no fence wait, no
	// supersession), and delivery returns the finalized body.
	committed := h.retryFreshUntil202(t, ctx, reservation.Reservation.LedgerID, "op-crash4", "pay_crash4", body)
	if _, err := h.backend.Status(ctx, h.resolved.ID, committed); err != nil {
		t.Fatalf("status after finalized resume: %v", err)
	}
	if states := h.attemptStates(t, ctx, "pay_crash4"); len(states) != 1 || states[0] != "promoted" {
		t.Fatalf("finalized payload must not be rebound; attempts=%v", states)
	}
	h.assertDeliverable(t, ctx, body)
}

// TestObjectRetrySupersessionCleanupRemovesObjects proves that after a
// supersession, driving the payload to deletion removes every ciphertext object,
// including the superseded ones.
func TestObjectRetrySupersessionCleanupRemovesObjects(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	store := objectstore.NewMemoryStore()
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
	backend, err := backendpg.New(ctx, pool, []runtime.Assignment{base}, newTestSealer(t), runtime.SystemClock{}, false,
		backendpg.WithPayloadStore(store), backendpg.WithStageWriteLease(200*time.Millisecond))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	resolved, _ := backend.ResolveCredential(ctx, base.CredentialID)

	// Fault the first attempt's final PUT so a superseding attempt is required,
	// leaving an orphaned staging object.
	store.SetPutFault(func(key string) error {
		if strings.Contains(key, "/attempts/0000000001/final/") {
			return errors.New("simulated crash after staging PUT")
		}
		return nil
	})

	body := []byte(`{"cleanup":"superseded"}`)
	hash := runtime.SHA256(body)
	headers := runtime.StableHeaders{EventID: "evt_cleanup", EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
	// Backdated acceptance so the payload becomes deletion-eligible quickly while
	// staying unexpired during the brief retry window.
	if _, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_cleanup", AssignmentID: resolved.ID, IdempotencyKey: "op-cleanup",
		BodySHA256: hash, Headers: headers, ReservedAt: runtime.SystemClock{}.Now().Add(-26 * time.Hour),
	}); err != nil {
		t.Fatal(err)
	}
	acceptedAt := runtime.SystemClock{}.Now().Add(-26 * time.Hour)
	payloadExpiry := runtime.SystemClock{}.Now().Add(6 * time.Second)

	accept := func(attempt int) error {
		sealed, _ := backend.Seal(body)
		ingestID := "ing_cleanup_" + strconv.Itoa(attempt)
		return backend.Accept(ctx, runtime.AcceptanceRequest{
			Assignment: resolved, LedgerID: "led_cleanup", IdempotencyKey: "op-cleanup",
			IngestID: ingestID, PayloadID: "pay_cleanup", DeliveryID: "del_" + ingestID, DispatchJobID: "job_" + ingestID,
			EndpointSnapshotID: resolved.EndpointSnapshotID, Payload: sealed, BodySHA256: hash, Headers: headers,
			AcceptedAt: acceptedAt, IdempotencyExpires: runtime.SystemClock{}.Now().Add(-time.Hour), PayloadExpiresAt: payloadExpiry,
			DispatchRunAt: acceptedAt, DispatchMaxAttempts: 5,
		})
	}
	deadline := time.Now().Add(4 * time.Second)
	for attempt := 0; ; attempt++ {
		err := accept(attempt)
		if err == nil {
			break
		}
		if !errors.Is(err, runtime.ErrUnavailable) {
			t.Fatalf("unexpected accept error: %v", err)
		}
		if time.Now().After(deadline) {
			t.Fatal("supersession retry never committed")
		}
		time.Sleep(30 * time.Millisecond)
	}
	if store.ObjectCount() < 2 {
		t.Fatalf("expected superseded + committed objects present before cleanup, got %d", store.ObjectCount())
	}

	// Deliver (terminal job), tombstone, wait for expiry, reclaim.
	job, ok, err := backend.ClaimJob(ctx, resolved.ID, runtime.JobKindDispatch, "worker", "wtok", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim: ok=%v err=%v", ok, err)
	}
	if err := backend.CompleteJob(ctx, runtime.JobLease{AssignmentID: resolved.ID, JobID: job.JobID, Owner: "worker", Token: "wtok", Attempt: job.Attempts}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.AdvanceTombstones(ctx, resolved.ID, 10); err != nil {
		t.Fatal(err)
	}
	waitForDatabaseTime(t, pool, payloadExpiry)
	reclaimed, err := backend.ReclaimExpiredPayloads(ctx, resolved.ID, 10)
	if err != nil {
		t.Fatal(err)
	}
	if reclaimed != 1 {
		t.Fatalf("expected 1 payload reclaimed, got %d", reclaimed)
	}
	if store.ObjectCount() != 0 {
		t.Fatalf("cleanup must remove every ciphertext object including superseded ones, %d remain", store.ObjectCount())
	}
}

// TestSupersedeObjectStageSingleWinner drives concurrent supersessions and
// asserts exactly one winner claims the payload; the others back off.
func TestSupersedeObjectStageSingleWinner(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	repo, err := durablepg.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	assignmentID := testAssignmentID(t)
	ledgerID, payloadID := seedStagingPayload(t, ctx, repo, assignmentID)

	const workers = 6
	var wg sync.WaitGroup
	var mu sync.Mutex
	winners := 0
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func(n int) {
			defer wg.Done()
			identity := freshStageIdentity(assignmentID, ledgerID, payloadID, []byte("winner-"+strconv.Itoa(n)))
			_, ok, err := repo.SupersedeObjectStage(ctx, identity, "owner-"+strconv.Itoa(n), "token-"+strconv.Itoa(n), time.Hour)
			if err != nil {
				t.Errorf("supersede error: %v", err)
				return
			}
			if ok {
				mu.Lock()
				winners++
				mu.Unlock()
			}
		}(i)
	}
	wg.Wait()
	if winners != 1 {
		t.Fatalf("expected exactly one supersession winner, got %d", winners)
	}
}

// TestSupersedeObjectStageFencesStaleWriter proves a stale writer holding a
// superseded attempt's lease can no longer finalize, while the superseding
// writer can.
func TestSupersedeObjectStageFencesStaleWriter(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	repo, err := durablepg.New(pool)
	if err != nil {
		t.Fatal(err)
	}
	assignmentID := testAssignmentID(t)
	ledgerID, payloadID := seedStagingPayload(t, ctx, repo, assignmentID)

	// A writer claims the initial attempt with a short lease, then "crashes".
	staleLease, ok, err := repo.ClaimObjectStageWrite(ctx, assignmentID, ledgerID, "stale", "stale-token", 200*time.Millisecond)
	if err != nil || !ok {
		t.Fatalf("stale claim: ok=%v err=%v", ok, err)
	}
	// After the lease expires, a retry supersedes the identity.
	time.Sleep(300 * time.Millisecond)
	identity := freshStageIdentity(assignmentID, ledgerID, payloadID, []byte("superseding-ciphertext"))
	newLease, superseded, err := repo.SupersedeObjectStage(ctx, identity, "winner", "winner-token", time.Hour)
	if err != nil || !superseded {
		t.Fatalf("supersede after fence: superseded=%v err=%v", superseded, err)
	}

	// The stale writer must not be able to finalize its superseded attempt.
	if err := repo.FinalizeObjectPayload(ctx, staleLease, durablepg.ObjectPromotion{
		ProviderStagingVersion: "stale-s", ProviderFinalVersion: "stale-f", Mode: "create_only",
	}); !errors.Is(err, durablepg.ErrLeaseLost) {
		t.Fatalf("stale writer must be fenced from finalizing, got %v", err)
	}
	// The superseding writer can finalize.
	if err := repo.FinalizeObjectPayload(ctx, newLease, durablepg.ObjectPromotion{
		ProviderStagingVersion: "win-s", ProviderFinalVersion: "win-f", Mode: "create_only",
	}); err != nil {
		t.Fatalf("superseding writer must be able to finalize: %v", err)
	}
}

// testAssignmentID mints a unique assignment identifier for a repository test.
func testAssignmentID(t *testing.T) string {
	t.Helper()
	return "asg_" + runtime.NewULIDSource(runtime.SystemClock{}).NewID("t")
}

// freshStageIdentity builds an object staging identity for ciphertext under a
// stable data encryption key (a real retry re-seals with a fresh random nonce;
// here distinct ciphertext yields a distinct checksum without reusing a GCM
// nonce for the same bytes).
func freshStageIdentity(assignmentID, ledgerID, payloadID string, ciphertext []byte) durablepg.ObjectPayloadStage {
	now := runtime.SystemClock{}.Now()
	nonce := runtime.SHA256(ciphertext)
	return durablepg.ObjectPayloadStage{
		AssignmentID:     assignmentID,
		LedgerID:         ledgerID,
		PayloadID:        payloadID,
		KeyReference:     "kms://cell/dek",
		KeyVersion:       "dek-stable-v1",
		Nonce:            nonce[:12],
		CiphertextSHA256: durablepg.SHA256(ciphertext),
		CiphertextSize:   int64(len(ciphertext)),
		ExpiresAt:        now.Add(48 * time.Hour),
		StagedAt:         now,
	}
}

// seedStagingPayload reserves a pending idempotency ledger and stages an
// object-store payload, leaving it in `staging` with a ready first attempt.
func seedStagingPayload(t *testing.T, ctx context.Context, repo *durablepg.Repository, assignmentID string) (ledgerID, payloadID string) {
	t.Helper()
	ids := runtime.NewULIDSource(runtime.SystemClock{})
	ledgerID = ids.NewID("led")
	payloadID = ids.NewID("pay")
	idemKey := ids.NewID("idem")
	body := []byte("seed-body")
	if _, err := repo.ReserveIdempotency(ctx, durablepg.ReservationRequest{
		LedgerID:       ledgerID,
		AssignmentID:   assignmentID,
		IdempotencyKey: idemKey,
		BodySHA256:     durablepg.SHA256(body),
		Headers:        durablepg.StableHeaders{EventID: "evt", EventType: "order.created", Route: "orders.primary", ContentType: "application/json"},
		ReservedAt:     runtime.SystemClock{}.Now(),
	}); err != nil {
		t.Fatal(err)
	}
	if err := repo.StageObjectPayload(ctx, freshStageIdentity(assignmentID, ledgerID, payloadID, []byte("seed-initial-ciphertext"))); err != nil {
		t.Fatal(err)
	}
	return ledgerID, payloadID
}
