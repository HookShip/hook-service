//go:build integration

package postgres

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationsFromZeroAndChecksumDrift(t *testing.T) {
	pool, _ := newIntegrationRepository(t)
	ctx := context.Background()

	var versionText string
	if err := pool.QueryRow(ctx, `SHOW server_version_num`).Scan(&versionText); err != nil {
		t.Fatal(err)
	}
	versionNumber, err := strconv.Atoi(versionText)
	if err != nil {
		t.Fatal(err)
	}
	if versionNumber < 170000 {
		t.Fatalf("PostgreSQL 17 or newer is required, got server_version_num %d", versionNumber)
	}
	var count int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM schema_migrations`).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count < 2 {
		t.Fatalf("expected the forward migrations to apply from zero, got %d", count)
	}
	if err := Migrate(ctx, pool); err != nil {
		t.Fatalf("idempotent migration run failed: %v", err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE schema_migrations
		SET checksum = repeat('0', 64)
		WHERE version = 1
	`); err != nil {
		t.Fatal(err)
	}
	err = Migrate(ctx, pool)
	if err == nil || !strings.Contains(err.Error(), "checksum drift") {
		t.Fatalf("expected checksum drift detection, got %v", err)
	}
}

func TestConcurrentIdempotencyReservation(t *testing.T) {
	_, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	request := ReservationRequest{
		AssignmentID:   testID("asg"),
		IdempotencyKey: "operation-01",
		BodySHA256:     SHA256([]byte("raw event bytes")),
		Headers: StableHeaders{
			EventID:     "event-01",
			EventType:   "order.created",
			Route:       "orders.primary",
			ContentType: "application/octet-stream",
		},
		ReservedAt: now,
	}

	const contenders = 12
	results := make(chan ReservationResult, contenders)
	errs := make(chan error, contenders)
	var wait sync.WaitGroup
	for index := 0; index < contenders; index++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			candidate := request
			candidate.LedgerID = testID("led")
			result, err := repository.ReserveIdempotency(ctx, candidate)
			if err != nil {
				errs <- err
				return
			}
			results <- result
		}()
	}
	wait.Wait()
	close(results)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}

	var ledgerID string
	created := 0
	seen := 0
	for result := range results {
		seen++
		if result.Status != ReservationPending {
			t.Fatalf("pending reservation was falsely reported as %s", result.Status)
		}
		if result.Created {
			created++
		}
		if ledgerID == "" {
			ledgerID = result.Reservation.LedgerID
		}
		if result.Reservation.LedgerID != ledgerID {
			t.Fatalf("concurrent callers observed different ledger IDs: %s and %s", ledgerID, result.Reservation.LedgerID)
		}
	}
	if seen != contenders || created != 1 {
		t.Fatalf("expected %d results and one creator, got %d results and %d creators", contenders, seen, created)
	}

	bodyConflict := request
	bodyConflict.LedgerID = testID("led")
	bodyConflict.BodySHA256 = SHA256([]byte("different raw bytes"))
	result, err := repository.ReserveIdempotency(ctx, bodyConflict)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReservationConflict {
		t.Fatalf("expected body conflict, got %s", result.Status)
	}

	metadataConflict := request
	metadataConflict.LedgerID = testID("led")
	metadataConflict.Headers.EventType = "order.updated"
	result, err = repository.ReserveIdempotency(ctx, metadataConflict)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReservationMetadataConflict {
		t.Fatalf("expected metadata conflict, got %s", result.Status)
	}

	snapshotID := putTestSnapshot(t, repository, request.AssignmentID, now)
	acceptance := testPostgresAcceptance(request.AssignmentID, request.IdempotencyKey, ledgerID, snapshotID, now)
	if err := repository.AcceptPostgres(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	duplicate := request
	duplicate.LedgerID = testID("led")
	result, err = repository.ReserveIdempotency(ctx, duplicate)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReservationDuplicate || result.Reservation.IngestID != acceptance.IngestID {
		t.Fatalf("expected original committed receipt, got %#v", result)
	}
}

func TestExpiredIdempotencyClassificationPrecedesConflicts(t *testing.T) {
	_, repository := newIntegrationRepository(t)
	ctx := context.Background()
	acceptedAt := time.Now().UTC().Add(-25 * time.Hour).Truncate(time.Second)
	request := testReservationRequest(acceptedAt)
	reservation, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := putTestSnapshot(t, repository, request.AssignmentID, acceptedAt)
	acceptance := testPostgresAcceptance(
		request.AssignmentID,
		request.IdempotencyKey,
		reservation.Reservation.LedgerID,
		snapshotID,
		acceptedAt,
	)
	if err := repository.AcceptPostgres(ctx, acceptance); err != nil {
		t.Fatal(err)
	}

	conflicting := request
	conflicting.LedgerID = testID("led")
	conflicting.BodySHA256 = SHA256([]byte("different raw bytes"))
	conflicting.Headers.EventType = "order.updated"
	result, err := repository.ReserveIdempotency(ctx, conflicting)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReservationExpired || result.Reservation.State != "tombstone" {
		t.Fatalf("expired conflicting retry was misclassified: %#v", result)
	}

	original := request
	original.LedgerID = testID("led")
	result, err = repository.ReserveIdempotency(ctx, original)
	if err != nil {
		t.Fatal(err)
	}
	if result.Status != ReservationExpired {
		t.Fatalf("expired matching retry was reported as %s", result.Status)
	}
}

func TestAtomicCommitBarrierAndPendingRecovery(t *testing.T) {
	pool, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	request := testReservationRequest(now)
	result, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}

	acceptance := testPostgresAcceptance(
		request.AssignmentID,
		request.IdempotencyKey,
		result.Reservation.LedgerID,
		testID("snap_missing"),
		now,
	)
	if err := repository.AcceptPostgres(ctx, acceptance); err == nil {
		t.Fatal("acceptance unexpectedly crossed the barrier without an endpoint snapshot")
	}
	assertCount(t, pool, `SELECT count(*) FROM event_ingests WHERE ingest_id = $1`, 0, acceptance.IngestID)
	assertCount(t, pool, `SELECT count(*) FROM payload_records WHERE payload_id = $1`, 0, acceptance.PayloadID)
	pending, err := repository.GetReservation(ctx, request.AssignmentID, request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" {
		t.Fatalf("failed barrier changed pending ledger to %s", pending.State)
	}

	acceptance.EndpointSnapshotID = putTestSnapshot(t, repository, request.AssignmentID, now)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := insertIngest(
		ctx,
		tx,
		result.Reservation,
		acceptance.IngestID,
		now,
		now.Add(24*time.Hour),
	); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO payload_records (
			payload_id,
			assignment_id,
			ledger_id,
			ingest_id,
			backend,
			state,
			ciphertext,
			key_reference,
			key_version,
			nonce,
			ciphertext_sha256,
			ciphertext_size,
			expires_at,
			created_at,
			committed_at
		) VALUES ($1, $2, $3, $4, 'postgres', 'committed', $5, $6, $7, $8, $9, $10, $11, $12, $12)
	`,
		acceptance.PayloadID,
		acceptance.AssignmentID,
		acceptance.LedgerID,
		acceptance.IngestID,
		acceptance.Ciphertext,
		acceptance.KeyReference,
		acceptance.KeyVersion,
		acceptance.Nonce,
		acceptance.CiphertextSHA256[:],
		len(acceptance.Ciphertext),
		acceptance.PayloadExpiresAt,
		now,
	); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := insertDispatch(
		ctx,
		tx,
		result.Reservation,
		acceptance.IngestID,
		acceptance.PayloadID,
		acceptance.DeliveryID,
		acceptance.DispatchJobID,
		acceptance.EndpointSnapshotID,
		now,
		acceptance.DispatchMaxAttempts,
		now,
	); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `
		UPDATE durable_jobs
		SET state = 'retry'
		WHERE assignment_id = $1 AND job_id = $2
	`, acceptance.AssignmentID, acceptance.DispatchJobID); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	if err := commitReservation(
		ctx,
		tx,
		result.Reservation,
		acceptance.IngestID,
		acceptance.PayloadID,
		acceptance.DispatchJobID,
		now,
		now.Add(24*time.Hour),
		now.Add(48*time.Hour),
	); err != nil {
		_ = tx.Rollback(ctx)
		t.Fatal(err)
	}
	err = tx.Commit(ctx)
	if err == nil {
		t.Fatal("database accepted a committed ledger without durable barrier artifacts")
	}
	var databaseError *pgconn.PgError
	if !errors.As(err, &databaseError) ||
		databaseError.Code != "23514" ||
		!strings.Contains(databaseError.Message, "committed idempotency ledger is missing the durable acceptance barrier") {
		t.Fatalf("expected only the deferred barrier trigger to reject the FK-valid fixture, got %v", err)
	}

	if err := repository.AcceptPostgres(ctx, acceptance); err != nil {
		t.Fatalf("pending recovery failed: %v", err)
	}
	assertCount(t, pool, `SELECT count(*) FROM event_ingests WHERE ingest_id = $1`, 1, acceptance.IngestID)
	assertCount(t, pool, `SELECT count(*) FROM payload_records WHERE payload_id = $1 AND state = 'committed'`, 1, acceptance.PayloadID)
	assertCount(t, pool, `SELECT count(*) FROM durable_jobs WHERE job_id = $1 AND state = 'ready'`, 1, acceptance.DispatchJobID)
	committed, err := repository.GetReservation(ctx, request.AssignmentID, request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if committed.State != "committed" {
		t.Fatalf("expected committed ledger, got %s", committed.State)
	}
}

func TestObjectPayloadRecoveryAndConflict(t *testing.T) {
	pool, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	request := testReservationRequest(now)
	reservation, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	payloadID := testID("pay")
	ciphertext := []byte("encrypted-object-ciphertext")
	stage := ObjectPayloadStage{
		AssignmentID:     request.AssignmentID,
		LedgerID:         reservation.Reservation.LedgerID,
		PayloadID:        payloadID,
		KeyReference:     "kms://regional-cell/key/7",
		KeyVersion:       "7",
		Nonce:            []byte("123456789012"),
		CiphertextSHA256: SHA256(ciphertext),
		CiphertextSize:   int64(len(ciphertext)),
		ExpiresAt:        now.Add(72 * time.Hour),
		StagedAt:         now,
	}
	if err := repository.StageObjectPayload(ctx, stage); err != nil {
		t.Fatal(err)
	}
	restarted, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	stored, err := restarted.GetObjectPayloadStage(
		ctx,
		stage.AssignmentID,
		stage.LedgerID,
	)
	if err != nil {
		t.Fatal(err)
	}
	expectedStaging, expectedFinal, err := ObjectKeys(stage.AssignmentID, stage.PayloadID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.PayloadID != stage.PayloadID ||
		stored.AssignmentID != stage.AssignmentID ||
		stored.LedgerID != stage.LedgerID ||
		stored.State != "staging" ||
		stored.KeyReference != stage.KeyReference ||
		stored.KeyVersion != stage.KeyVersion ||
		!bytes.Equal(stored.Nonce, stage.Nonce) ||
		stored.CiphertextSHA256 != stage.CiphertextSHA256 ||
		stored.CiphertextSize != stage.CiphertextSize ||
		stored.StagingObjectKey != expectedStaging ||
		stored.FinalObjectKey != expectedFinal ||
		!stored.ExpiresAt.Equal(stage.ExpiresAt) ||
		!stored.StagedAt.Equal(stage.StagedAt) {
		t.Fatalf("restarted repository loaded different object stage: %#v", stored)
	}
	if _, err := restarted.GetObjectPayloadStage(ctx, testID("asg_foreign"), stage.LedgerID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("foreign assignment observed object stage: %v", err)
	}
	if err := restarted.StageObjectPayload(ctx, stage); err != nil {
		t.Fatalf("identical pending recovery was not idempotent: %v", err)
	}
	conflicting := stage
	conflicting.KeyVersion = "8"
	if err := restarted.StageObjectPayload(ctx, conflicting); !errors.Is(err, ErrPayloadConflict) {
		t.Fatalf("expected restarted encryption metadata conflict, got %v", err)
	}
	conflicting = stage
	conflicting.ExpiresAt = stage.ExpiresAt.Add(time.Hour)
	if err := restarted.StageObjectPayload(ctx, conflicting); !errors.Is(err, ErrPayloadConflict) {
		t.Fatalf("expected object stage expiry conflict, got %v", err)
	}
	var ciphertextIsNull bool
	var stagingKey, finalKey string
	if err := pool.QueryRow(ctx, `
		SELECT ciphertext IS NULL, staging_object_key, final_object_key
		FROM payload_records
		WHERE assignment_id = $1 AND payload_id = $2
	`, stage.AssignmentID, stage.PayloadID).Scan(&ciphertextIsNull, &stagingKey, &finalKey); err != nil {
		t.Fatal(err)
	}
	if !ciphertextIsNull || stagingKey != expectedStaging || finalKey != expectedFinal {
		t.Fatal("object payload did not preserve deterministic payload-free database metadata")
	}

	acceptance := ObjectAcceptance{
		AssignmentID:        request.AssignmentID,
		IdempotencyKey:      request.IdempotencyKey,
		LedgerID:            reservation.Reservation.LedgerID,
		IngestID:            testID("ing"),
		PayloadID:           payloadID,
		DeliveryID:          testID("del"),
		DispatchJobID:       testID("job"),
		EndpointSnapshotID:  putTestSnapshot(t, repository, request.AssignmentID, now),
		AcceptedAt:          now,
		IdempotencyExpires:  now.Add(24 * time.Hour),
		DispatchRunAt:       now,
		DispatchMaxAttempts: 5,
	}
	if err := repository.AcceptFinalizedObject(ctx, acceptance); err == nil {
		t.Fatal("unfinalized object payload crossed the acceptance barrier")
	}
	writeLease, ok, err := repository.ClaimObjectStageWrite(
		ctx,
		stage.AssignmentID,
		stage.LedgerID,
		"object-writer",
		"object-write-token",
		5*time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("failed to claim object stage write: %#v %v %v", writeLease, ok, err)
	}
	if err := repository.FinalizeObjectPayload(
		ctx,
		writeLease,
		ObjectPromotion{
			ProviderStagingVersion: "stage-version-1",
			ProviderFinalVersion:   "final-version-1",
			Mode:                   "create_only",
		},
	); err != nil {
		t.Fatal(err)
	}
	finalized, err := restarted.GetObjectPayloadStage(ctx, stage.AssignmentID, stage.LedgerID)
	if err != nil {
		t.Fatal(err)
	}
	if finalized.State != "finalized" || finalized.FinalizedAt == nil {
		t.Fatalf("finalized object stage was not restart-readable: %#v", finalized)
	}
	if err := repository.AcceptFinalizedObject(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `
		SELECT count(*)
		FROM payload_records
		WHERE payload_id = $1 AND state = 'committed' AND ciphertext IS NULL AND ingest_id = $2
	`, 1, payloadID, acceptance.IngestID)
}

func TestFinalizedExpiredObjectCannotCommit(t *testing.T) {
	pool, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC()
	acceptedAt := now.Add(-25 * time.Hour).Truncate(time.Second)
	request := testReservationRequest(acceptedAt)
	reservation, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte("finalized ciphertext that expires before commit")
	stage := ObjectPayloadStage{
		AssignmentID:     request.AssignmentID,
		LedgerID:         reservation.Reservation.LedgerID,
		PayloadID:        testID("pay"),
		KeyReference:     "kms://regional-cell/key/expired",
		KeyVersion:       "expired-v1",
		Nonce:            []byte("expirednonce"),
		CiphertextSHA256: SHA256(ciphertext),
		CiphertextSize:   int64(len(ciphertext)),
		ExpiresAt:        now.Add(2 * time.Second),
		StagedAt:         acceptedAt,
	}
	if err := repository.StageObjectPayload(ctx, stage); err != nil {
		t.Fatal(err)
	}
	writeLease, ok, err := repository.ClaimObjectStageWrite(
		ctx,
		stage.AssignmentID,
		stage.LedgerID,
		"expiring-writer",
		"expiring-token",
		time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("failed to claim expiring stage: %#v %v %v", writeLease, ok, err)
	}
	if err := repository.FinalizeObjectPayload(ctx, writeLease, ObjectPromotion{
		ProviderStagingVersion: "expired-stage-version",
		ProviderFinalVersion:   "expired-final-version",
		Mode:                   "create_only",
	}); err != nil {
		t.Fatal(err)
	}
	waitForDatabaseTime(t, pool, stage.ExpiresAt)

	acceptance := ObjectAcceptance{
		AssignmentID:        request.AssignmentID,
		IdempotencyKey:      request.IdempotencyKey,
		LedgerID:            reservation.Reservation.LedgerID,
		IngestID:            testID("ing"),
		PayloadID:           stage.PayloadID,
		DeliveryID:          testID("del"),
		DispatchJobID:       testID("job"),
		EndpointSnapshotID:  putTestSnapshot(t, repository, request.AssignmentID, acceptedAt),
		AcceptedAt:          acceptedAt,
		IdempotencyExpires:  acceptedAt.Add(24 * time.Hour),
		DispatchRunAt:       acceptedAt,
		DispatchMaxAttempts: 5,
	}
	err = repository.AcceptFinalizedObject(ctx, acceptance)
	if err == nil || !strings.Contains(err.Error(), "expired") {
		t.Fatalf("finalized expired object crossed commit barrier: %v", err)
	}
	pending, err := repository.GetReservation(ctx, request.AssignmentID, request.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if pending.State != "pending" {
		t.Fatalf("expired object commit changed ledger to %s", pending.State)
	}
	assertCount(t, pool, `SELECT count(*) FROM event_ingests WHERE ingest_id = $1`, 0, acceptance.IngestID)
}

func TestObjectStageWriteLeaseFencingAndCleanupDiscovery(t *testing.T) {
	t.Run("claim reclaim stale finalize and retained cleanup keys", func(t *testing.T) {
		pool, repository := newIntegrationRepository(t)
		ctx := context.Background()
		now := time.Now().UTC()
		acceptedAt := now.Add(-25 * time.Hour).Truncate(time.Second)
		request := testReservationRequest(acceptedAt)
		reservation, err := repository.ReserveIdempotency(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext := []byte("stable ciphertext across fenced attempts")
		stage := ObjectPayloadStage{
			AssignmentID:     request.AssignmentID,
			LedgerID:         reservation.Reservation.LedgerID,
			PayloadID:        testID("pay"),
			KeyReference:     "kms://regional-cell/key/fenced",
			KeyVersion:       "fenced-v1",
			Nonce:            []byte("fenced-nonce"),
			CiphertextSHA256: SHA256(ciphertext),
			CiphertextSize:   int64(len(ciphertext)),
			ExpiresAt:        now.Add(3 * time.Second),
			StagedAt:         acceptedAt,
		}
		if err := repository.StageObjectPayload(ctx, stage); err != nil {
			t.Fatal(err)
		}
		first, ok, err := repository.ClaimObjectStageWrite(
			ctx,
			stage.AssignmentID,
			stage.LedgerID,
			"stage-writer-a",
			"stage-token-a",
			5*time.Minute,
		)
		if err != nil || !ok {
			t.Fatalf("initial stage write claim failed: %#v %v %v", first, ok, err)
		}
		if _, ok, err := repository.ClaimObjectStageWrite(
			ctx,
			stage.AssignmentID,
			stage.LedgerID,
			"stage-writer-b",
			"stage-token-b",
			5*time.Minute,
		); err != nil || ok {
			t.Fatalf("active stage write lease was concurrently claimed: ok=%v err=%v", ok, err)
		}
		expireObjectStageLease(t, pool, first.StageAttemptID)
		reclaimed, ok, err := repository.ClaimObjectStageWrite(
			ctx,
			stage.AssignmentID,
			stage.LedgerID,
			"stage-writer-b",
			"stage-token-b",
			5*time.Minute,
		)
		if err != nil || !ok || reclaimed.Attempt != 2 {
			t.Fatalf("stage write lease was not reclaimed: %#v %v %v", reclaimed, ok, err)
		}
		if reclaimed.StagingObjectKey == first.StagingObjectKey ||
			reclaimed.FinalObjectKey == first.FinalObjectKey {
			t.Fatal("reclaimed stage write reused a stale attempt object key")
		}
		if _, err := pool.Exec(ctx, `
			UPDATE payload_records
			SET staging_object_key = $3, final_object_key = $4
			WHERE assignment_id = $1 AND payload_id = $2
		`,
			stage.AssignmentID,
			stage.PayloadID,
			first.StagingObjectKey,
			first.FinalObjectKey,
		); err == nil {
			t.Fatal("payload object identity accepted an ABA reset to an orphaned attempt")
		}
		if err := repository.FinalizeObjectPayload(ctx, first, ObjectPromotion{
			ProviderStagingVersion: "stale-stage-version",
			ProviderFinalVersion:   "stale-final-version",
			Mode:                   "create_only",
		}); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale stage writer was not fenced: %v", err)
		}
		if err := repository.FinalizeObjectPayload(ctx, reclaimed, ObjectPromotion{
			ProviderStagingVersion: "current-stage-version",
			ProviderFinalVersion:   "current-final-version",
			Mode:                   "create_only",
		}); err != nil {
			t.Fatal(err)
		}

		acceptance := ObjectAcceptance{
			AssignmentID:        request.AssignmentID,
			IdempotencyKey:      request.IdempotencyKey,
			LedgerID:            reservation.Reservation.LedgerID,
			IngestID:            testID("ing"),
			PayloadID:           stage.PayloadID,
			DeliveryID:          testID("del"),
			DispatchJobID:       testID("job"),
			EndpointSnapshotID:  putTestSnapshot(t, repository, request.AssignmentID, acceptedAt),
			AcceptedAt:          acceptedAt,
			IdempotencyExpires:  acceptedAt.Add(24 * time.Hour),
			DispatchRunAt:       acceptedAt,
			DispatchMaxAttempts: 5,
		}
		if err := repository.AcceptFinalizedObject(ctx, acceptance); err != nil {
			t.Fatal(err)
		}
		waitForDatabaseTime(t, pool, stage.ExpiresAt)
		preparePayloadDeletion(t, repository, request.AssignmentID)
		deletion, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			request.AssignmentID,
			"stage-cleanup-worker",
			"stage-cleanup-token",
			5*time.Minute,
		)
		if err != nil || !ok {
			t.Fatalf("failed to claim stage cleanup deletion: %#v %v %v", deletion, ok, err)
		}
		if err := repository.CompletePayloadDeletion(ctx, deletion); err != nil {
			t.Fatal(err)
		}
		cleanup, err := repository.ListObjectStageCleanup(ctx, request.AssignmentID, stage.PayloadID)
		if err != nil {
			t.Fatal(err)
		}
		if len(cleanup) != 2 ||
			cleanup[0].StagingObjectKey != first.StagingObjectKey ||
			cleanup[0].State != "orphaned" ||
			cleanup[1].StagingObjectKey != reclaimed.StagingObjectKey ||
			cleanup[1].State != "promoted" ||
			cleanup[1].ProviderStagingVersion != "current-stage-version" ||
			cleanup[1].ProviderFinalVersion != "current-final-version" {
			t.Fatalf("stale write identities were not retained after deletion: %#v", cleanup)
		}
	})

	t.Run("active writer excludes orphan and deletion", func(t *testing.T) {
		pool, repository := newIntegrationRepository(t)
		ctx := context.Background()
		now := time.Now().UTC()
		request := testReservationRequest(now.Add(-25 * time.Hour))
		reservation, err := repository.ReserveIdempotency(ctx, request)
		if err != nil {
			t.Fatal(err)
		}
		ciphertext := []byte("active writer exclusion ciphertext")
		stage := ObjectPayloadStage{
			AssignmentID:     request.AssignmentID,
			LedgerID:         reservation.Reservation.LedgerID,
			PayloadID:        testID("pay"),
			KeyReference:     "kms://regional-cell/key/exclusion",
			KeyVersion:       "exclusion-v1",
			Nonce:            []byte("exclude-nonce"),
			CiphertextSHA256: SHA256(ciphertext),
			CiphertextSize:   int64(len(ciphertext)),
			ExpiresAt:        now.Add(time.Second),
			StagedAt:         now.Add(-25 * time.Hour),
		}
		if err := repository.StageObjectPayload(ctx, stage); err != nil {
			t.Fatal(err)
		}
		writeLease, ok, err := repository.ClaimObjectStageWrite(
			ctx,
			stage.AssignmentID,
			stage.LedgerID,
			"active-stage-writer",
			"active-stage-token",
			5*time.Minute,
		)
		if err != nil || !ok {
			t.Fatalf("failed to claim active stage writer: %#v %v %v", writeLease, ok, err)
		}
		waitForDatabaseTime(t, pool, stage.ExpiresAt)
		if count, err := repository.MarkExpiredObjectStagesOrphaned(
			ctx,
			stage.AssignmentID,
			100,
		); err != nil || count != 0 {
			t.Fatalf("retention orphaned an active writer: count=%d err=%v", count, err)
		}
		if _, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			stage.AssignmentID,
			"early-delete-worker",
			"early-delete-token",
			time.Minute,
		); err != nil || ok {
			t.Fatalf("deletion claimed payload with active writer: ok=%v err=%v", ok, err)
		}
		expireObjectStageLease(t, pool, writeLease.StageAttemptID)
		if count, err := repository.MarkExpiredObjectStagesOrphaned(
			ctx,
			stage.AssignmentID,
			100,
		); err != nil || count != 1 {
			t.Fatalf("expired writer did not become orphan-cleanable: count=%d err=%v", count, err)
		}
		deletion, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			stage.AssignmentID,
			"orphan-delete-worker",
			"orphan-delete-token",
			time.Minute,
		)
		if err != nil || !ok {
			t.Fatalf("orphan deletion was not claimable: %#v %v %v", deletion, ok, err)
		}
		if err := repository.CompletePayloadDeletion(ctx, deletion); err != nil {
			t.Fatal(err)
		}
	})
}

func TestJobLeaseClaimReclaimFencingAndSkipLocked(t *testing.T) {
	pool, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	assignmentID := testID("asg")

	firstJobID := seedAcceptedPostgres(t, repository, assignmentID, now.Add(-time.Minute), 3).DispatchJobID
	first, ok, err := repository.ClaimJob(
		ctx,
		assignmentID,
		"dispatch",
		"worker-a",
		"token-a",
		5*time.Minute,
	)
	if err != nil || !ok || first.JobID != firstJobID || first.Attempts != 1 {
		t.Fatalf("unexpected initial claim: job=%#v ok=%v err=%v", first, ok, err)
	}
	if _, ok, err := repository.ClaimJob(
		ctx,
		assignmentID,
		"dispatch",
		"worker-b",
		"token-b",
		5*time.Minute,
	); err != nil || ok {
		t.Fatalf("active lease was reclaimed: ok=%v err=%v", ok, err)
	}
	expireJobLease(t, pool, firstJobID)
	reclaimed, ok, err := repository.ClaimJob(
		ctx,
		assignmentID,
		"dispatch",
		"worker-b",
		"token-b",
		5*time.Minute,
	)
	if err != nil || !ok || reclaimed.JobID != firstJobID || reclaimed.Attempts != 2 {
		t.Fatalf("stale lease was not reclaimed: job=%#v ok=%v err=%v", reclaimed, ok, err)
	}
	oldLease := JobLease{
		AssignmentID: assignmentID,
		JobID:        firstJobID,
		Owner:        "worker-a",
		Token:        "token-a",
		Attempt:      1,
	}
	if err := repository.CompleteJob(ctx, oldLease); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale worker was not fenced: %v", err)
	}
	newLease := JobLease{
		AssignmentID: assignmentID,
		JobID:        firstJobID,
		Owner:        "worker-b",
		Token:        "token-b",
		Attempt:      2,
	}
	if err := repository.CompleteJob(ctx, newLease); err != nil {
		t.Fatal(err)
	}

	exhausted := seedAcceptedPostgres(t, repository, assignmentID, now.Add(-2*time.Minute), 1)
	claimed, ok, err := repository.ClaimJob(
		ctx,
		assignmentID,
		"dispatch",
		"worker-c",
		"token-c",
		5*time.Minute,
	)
	if err != nil || !ok || claimed.JobID != exhausted.DispatchJobID {
		t.Fatalf("failed to claim max-attempt job: %#v %v %v", claimed, ok, err)
	}
	if err := repository.RetryJob(ctx, JobLease{
		AssignmentID: assignmentID,
		JobID:        claimed.JobID,
		Owner:        claimed.LeaseOwner,
		Token:        claimed.LeaseToken,
		Attempt:      claimed.Attempts,
	}, time.Minute, "network_error"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM durable_jobs WHERE job_id = $1 AND state = 'dead'`, 1, exhausted.DispatchJobID)

	second := seedAcceptedPostgres(t, repository, assignmentID, now.Add(-3*time.Minute), 3)
	third := seedAcceptedPostgres(t, repository, assignmentID, now.Add(-3*time.Minute), 3)
	claimedIDs := make(chan string, 2)
	errs := make(chan error, 2)
	var wait sync.WaitGroup
	for index := 0; index < 2; index++ {
		wait.Add(1)
		go func(index int) {
			defer wait.Done()
			job, ok, err := repository.ClaimJob(
				ctx,
				assignmentID,
				"dispatch",
				fmt.Sprintf("worker-%d", index),
				fmt.Sprintf("token-%d", index),
				time.Minute,
			)
			if err != nil {
				errs <- err
				return
			}
			if !ok {
				errs <- errors.New("concurrent SKIP LOCKED claim returned no job")
				return
			}
			claimedIDs <- job.JobID
		}(index)
	}
	wait.Wait()
	close(claimedIDs)
	close(errs)
	for err := range errs {
		t.Fatal(err)
	}
	seen := map[string]bool{}
	for jobID := range claimedIDs {
		seen[jobID] = true
	}
	if !seen[second.DispatchJobID] || !seen[third.DispatchJobID] || len(seen) != 2 {
		t.Fatalf("SKIP LOCKED claims were not distinct: %#v", seen)
	}
}

func TestImmutableLineageAssignmentIsolationAndOutbox(t *testing.T) {
	pool, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)
	assignmentID := testID("asg")
	acceptance := seedAcceptedPostgres(t, repository, assignmentID, now, 5)

	if _, err := pool.Exec(ctx, `
		UPDATE endpoint_snapshots SET content = $3
		WHERE assignment_id = $1 AND endpoint_snapshot_id = $2
	`, assignmentID, acceptance.EndpointSnapshotID, []byte(`{"changed":true}`)); err == nil {
		t.Fatal("endpoint snapshot mutation succeeded")
	}
	if _, err := repository.PutEndpointSnapshot(ctx, EndpointSnapshot{
		SnapshotID:       acceptance.EndpointSnapshotID,
		AssignmentID:     assignmentID,
		EndpointID:       testID("endpoint"),
		BundleID:         testID("cfg"),
		ConfigGeneration: 8,
		Content:          []byte(`{"different":true}`),
		CreatedAt:        now,
	}); !errors.Is(err, ErrSnapshotConflict) {
		t.Fatalf("expected immutable snapshot conflict, got %v", err)
	}
	foreignExists, err := repository.IngestExists(ctx, testID("asg_foreign"), acceptance.IngestID)
	if err != nil {
		t.Fatal(err)
	}
	if foreignExists {
		t.Fatal("foreign assignment observed an ingest")
	}
	if _, err := pool.Exec(ctx, `
		INSERT INTO deliveries (
			delivery_id,
			assignment_id,
			ingest_id,
			payload_id,
			endpoint_snapshot_id,
			state,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, 'ready', $6, $6)
	`,
		testID("del"),
		testID("asg_foreign"),
		acceptance.IngestID,
		acceptance.PayloadID,
		acceptance.EndpointSnapshotID,
		now,
	); err == nil {
		t.Fatal("cross-assignment delivery lineage succeeded")
	}

	status := 503
	attempt := AttemptRecord{
		AttemptID:          testID("att"),
		AssignmentID:       assignmentID,
		DeliveryID:         acceptance.DeliveryID,
		JobID:              acceptance.DispatchJobID,
		EndpointSnapshotID: acceptance.EndpointSnapshotID,
		AttemptNumber:      1,
		Outcome:            "retryable_failure",
		ResponseStatus:     &status,
		ErrorCode:          "upstream_unavailable",
		DurationMS:         125,
		StartedAt:          now.Add(-2 * time.Minute),
		FinishedAt:         now.Add(-2*time.Minute + 125*time.Millisecond),
	}
	outbox := OutboxRecord{
		OutboxID:           testID("out"),
		AssignmentID:       assignmentID,
		DeliveryID:         acceptance.DeliveryID,
		AttemptID:          attempt.AttemptID,
		EndpointSnapshotID: acceptance.EndpointSnapshotID,
		SchemaVersion:      "2026-07-01",
		EventKind:          "delivery_attempt",
		Outcome:            attempt.Outcome,
		ResponseStatus:     attempt.ResponseStatus,
		ErrorCode:          attempt.ErrorCode,
		OccurredAt:         attempt.FinishedAt,
		RunAt:              attempt.FinishedAt,
		MaxAttempts:        3,
	}
	if err := repository.RecordAttemptAndOutbox(ctx, attempt, outbox); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		UPDATE delivery_attempts SET outcome = 'succeeded'
		WHERE assignment_id = $1 AND attempt_id = $2
	`, assignmentID, attempt.AttemptID); err == nil {
		t.Fatal("delivery attempt mutation succeeded")
	}
	var forbiddenColumns int
	if err := pool.QueryRow(ctx, `
		SELECT count(*)
		FROM information_schema.columns
		WHERE table_schema = current_schema()
		  AND table_name IN ('delivery_attempts', 'metadata_outbox')
		  AND (
			  column_name LIKE '%payload%'
			  OR column_name LIKE '%ciphertext%'
			  OR column_name LIKE '%response_body%'
		  )
	`).Scan(&forbiddenColumns); err != nil {
		t.Fatal(err)
	}
	if forbiddenColumns != 0 {
		t.Fatalf("attempt/outbox schema contains %d forbidden payload columns", forbiddenColumns)
	}

	first, ok, err := repository.ClaimOutbox(
		ctx,
		assignmentID,
		"metadata-worker-a",
		"outbox-token-a",
		5*time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("outbox claim failed: %#v %v %v", first, ok, err)
	}
	expireOutboxLease(t, pool, first.OutboxID)
	reclaimed, ok, err := repository.ClaimOutbox(
		ctx,
		assignmentID,
		"metadata-worker-b",
		"outbox-token-b",
		5*time.Minute,
	)
	if err != nil || !ok || reclaimed.OutboxID != first.OutboxID || reclaimed.Attempts != 2 {
		t.Fatalf("outbox reclaim failed: %#v %v %v", reclaimed, ok, err)
	}
	if err := repository.PublishOutbox(ctx, OutboxLease{
		AssignmentID: assignmentID,
		OutboxID:     first.OutboxID,
		Owner:        first.LeaseOwner,
		Token:        first.LeaseToken,
		Attempt:      first.Attempts,
	}); !errors.Is(err, ErrLeaseLost) {
		t.Fatalf("stale outbox publisher was not fenced: %v", err)
	}
	if err := repository.PublishOutbox(ctx, OutboxLease{
		AssignmentID: assignmentID,
		OutboxID:     reclaimed.OutboxID,
		Owner:        reclaimed.LeaseOwner,
		Token:        reclaimed.LeaseToken,
		Attempt:      reclaimed.Attempts,
	}); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `SELECT count(*) FROM metadata_outbox WHERE outbox_id = $1 AND state = 'published'`, 1, outbox.OutboxID)

	secondAttempt := attempt
	secondAttempt.AttemptID = testID("att")
	secondAttempt.AttemptNumber = 2
	secondAttempt.StartedAt = attempt.FinishedAt.Add(time.Second)
	secondAttempt.FinishedAt = secondAttempt.StartedAt.Add(50 * time.Millisecond)
	secondOutbox := outbox
	secondOutbox.OutboxID = testID("out")
	secondOutbox.AttemptID = secondAttempt.AttemptID
	secondOutbox.OccurredAt = secondAttempt.FinishedAt
	secondOutbox.RunAt = secondAttempt.FinishedAt
	secondOutbox.MaxAttempts = 1
	if err := repository.RecordAttemptAndOutbox(ctx, secondAttempt, secondOutbox); err != nil {
		t.Fatal(err)
	}
	deadCandidate, ok, err := repository.ClaimOutbox(
		ctx,
		assignmentID,
		"metadata-worker-c",
		"outbox-token-c",
		5*time.Minute,
	)
	if err != nil || !ok || deadCandidate.OutboxID != secondOutbox.OutboxID {
		t.Fatalf("failed to claim max-attempt outbox row: %#v %v %v", deadCandidate, ok, err)
	}
	if err := repository.RetryOutbox(ctx, OutboxLease{
		AssignmentID: assignmentID,
		OutboxID:     deadCandidate.OutboxID,
		Owner:        deadCandidate.LeaseOwner,
		Token:        deadCandidate.LeaseToken,
		Attempt:      deadCandidate.Attempts,
	}, time.Minute, "publish_failed"); err != nil {
		t.Fatal(err)
	}
	assertCount(t, pool, `
		SELECT count(*)
		FROM metadata_outbox
		WHERE outbox_id = $1 AND state = 'dead' AND completed_at IS NOT NULL
	`, 1, secondOutbox.OutboxID)
}

func TestRetentionTombstonesAndOrphansUseDatabaseClock(t *testing.T) {
	_, repository := newIntegrationRepository(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Second)

	activeAssignment := testID("asg")
	seedAcceptedPostgres(t, repository, activeAssignment, now, 5)
	if count, err := repository.AdvanceTombstones(ctx, activeAssignment, 100); err != nil || count != 0 {
		t.Fatalf("unexpired ledger tombstoned: count=%d err=%v", count, err)
	}

	expiredAssignment := testID("asg")
	acceptance := seedAcceptedPostgres(t, repository, expiredAssignment, now.Add(-25*time.Hour), 5)
	if count, err := repository.AdvanceTombstones(ctx, expiredAssignment, 100); err != nil || count != 1 {
		t.Fatalf("expired ledger did not tombstone: count=%d err=%v", count, err)
	}
	reservation, err := repository.GetReservation(ctx, expiredAssignment, acceptance.IdempotencyKey)
	if err != nil {
		t.Fatal(err)
	}
	if reservation.State != "tombstone" ||
		reservation.TombstoneExpiresAt == nil ||
		reservation.TombstoneExpiresAt.Before(now.Add(22*time.Hour)) {
		t.Fatalf("invalid tombstone retention: %#v", reservation)
	}

	pendingRequest := testReservationRequest(now.Add(-25 * time.Hour))
	pendingRequest.AssignmentID = expiredAssignment
	pendingRequest.IdempotencyKey = "orphan-operation"
	pending, err := repository.ReserveIdempotency(ctx, pendingRequest)
	if err != nil {
		t.Fatal(err)
	}
	stage := ObjectPayloadStage{
		AssignmentID:     expiredAssignment,
		LedgerID:         pending.Reservation.LedgerID,
		PayloadID:        testID("pay"),
		KeyReference:     "kms://regional-cell/key/orphan",
		KeyVersion:       "orphan",
		Nonce:            []byte("abcdefghijkl"),
		CiphertextSHA256: SHA256([]byte("orphan ciphertext")),
		CiphertextSize:   int64(len("orphan ciphertext")),
		ExpiresAt:        now.Add(-time.Hour),
		StagedAt:         now.Add(-25 * time.Hour),
	}
	if err := repository.StageObjectPayload(ctx, stage); err != nil {
		t.Fatal(err)
	}
	if count, err := repository.MarkExpiredObjectStagesOrphaned(
		ctx,
		expiredAssignment,
		100,
	); err != nil || count != 1 {
		t.Fatalf("object stage orphan transition failed: count=%d err=%v", count, err)
	}
	if err := repository.StageObjectPayload(ctx, stage); !errors.Is(err, ErrPayloadUnavailable) {
		t.Fatalf("orphaned stage was falsely reported recoverable: %v", err)
	}
}

func TestPayloadDeletionLeaseReclaimAndFencing(t *testing.T) {
	t.Run("postgres ciphertext", func(t *testing.T) {
		pool, repository := newIntegrationRepository(t)
		ctx := context.Background()
		assignmentID := testID("asg")
		acceptedAt := time.Now().UTC().Add(-25 * time.Hour).Truncate(time.Second)
		acceptance := seedAcceptedPostgres(t, repository, assignmentID, acceptedAt, 5)
		preparePayloadDeletion(t, repository, assignmentID)

		first, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			assignmentID,
			"payload-worker-a",
			"payload-token-a",
			5*time.Minute,
		)
		if err != nil || !ok || first.PayloadID != acceptance.PayloadID || first.Backend != "postgres" {
			t.Fatalf("unexpected PostgreSQL deletion claim: %#v %v %v", first, ok, err)
		}
		assertCount(t, pool, `
			SELECT count(*)
			FROM payload_records
			WHERE payload_id = $1 AND state = 'deleting' AND ciphertext IS NOT NULL
		`, 1, acceptance.PayloadID)
		if _, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			assignmentID,
			"payload-worker-b",
			"payload-token-b",
			5*time.Minute,
		); err != nil || ok {
			t.Fatalf("active payload deletion lease was reclaimed: ok=%v err=%v", ok, err)
		}
		expirePayloadDeletionLease(t, pool, first.PayloadID)
		reclaimed, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			assignmentID,
			"payload-worker-b",
			"payload-token-b",
			5*time.Minute,
		)
		if err != nil || !ok || reclaimed.Attempt != 2 {
			t.Fatalf("PostgreSQL deletion lease was not reclaimed: %#v %v %v", reclaimed, ok, err)
		}
		assertCount(t, pool, `
			SELECT count(*)
			FROM payload_records
			WHERE payload_id = $1 AND state = 'deleting' AND ciphertext IS NOT NULL
		`, 1, acceptance.PayloadID)
		if err := repository.CompletePayloadDeletion(ctx, first); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale PostgreSQL deletion completion was not fenced: %v", err)
		}
		if err := repository.CompletePayloadDeletion(ctx, reclaimed); err != nil {
			t.Fatal(err)
		}
		assertCount(t, pool, `
			SELECT count(*)
			FROM payload_records
			WHERE payload_id = $1 AND state = 'deleted' AND ciphertext IS NULL
		`, 1, acceptance.PayloadID)
	})

	t.Run("object store", func(t *testing.T) {
		pool, repository := newIntegrationRepository(t)
		ctx := context.Background()
		assignmentID := testID("asg")
		acceptedAt := time.Now().UTC().Add(-25 * time.Hour).Truncate(time.Second)
		acceptance, stage := seedAcceptedObject(t, repository, assignmentID, acceptedAt)
		waitForDatabaseTime(t, pool, stage.ExpiresAt)
		preparePayloadDeletion(t, repository, assignmentID)

		first, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			assignmentID,
			"object-delete-a",
			"object-delete-token-a",
			5*time.Minute,
		)
		if err != nil || !ok || first.PayloadID != acceptance.PayloadID || first.Backend != "object_store" {
			t.Fatalf("unexpected object deletion claim: %#v %v %v", first, ok, err)
		}
		expectedStaging, expectedFinal, err := ObjectKeys(assignmentID, stage.PayloadID)
		if err != nil {
			t.Fatal(err)
		}
		if first.StagingObjectKey != expectedStaging || first.FinalObjectKey != expectedFinal {
			t.Fatalf("deletion lease lost deterministic object identity: %#v", first)
		}
		expirePayloadDeletionLease(t, pool, first.PayloadID)
		reclaimed, ok, err := repository.ClaimPayloadDeletion(
			ctx,
			assignmentID,
			"object-delete-b",
			"object-delete-token-b",
			5*time.Minute,
		)
		if err != nil || !ok || reclaimed.Attempt != 2 ||
			reclaimed.StagingObjectKey != expectedStaging ||
			reclaimed.FinalObjectKey != expectedFinal {
			t.Fatalf("object deletion lease was not resumed exactly: %#v %v %v", reclaimed, ok, err)
		}
		if err := repository.CompletePayloadDeletion(ctx, first); !errors.Is(err, ErrLeaseLost) {
			t.Fatalf("stale object deletion completion was not fenced: %v", err)
		}
		if err := repository.CompletePayloadDeletion(ctx, reclaimed); err != nil {
			t.Fatal(err)
		}
		assertCount(t, pool, `
			SELECT count(*)
			FROM payload_records
			WHERE payload_id = $1 AND state = 'deleted' AND ciphertext IS NULL
		`, 1, acceptance.PayloadID)
	})
}

func preparePayloadDeletion(
	t *testing.T,
	repository *Repository,
	assignmentID string,
) {
	t.Helper()
	ctx := context.Background()
	if count, err := repository.AdvanceTombstones(ctx, assignmentID, 100); err != nil || count != 1 {
		t.Fatalf("failed to tombstone deletion fixture: count=%d err=%v", count, err)
	}
	job, ok, err := repository.ClaimJob(
		ctx,
		assignmentID,
		"dispatch",
		"deletion-dispatch-worker",
		testID("dispatch_token"),
		5*time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("failed to claim deletion fixture dispatch: %#v %v %v", job, ok, err)
	}
	if err := repository.CompleteJob(ctx, JobLease{
		AssignmentID: assignmentID,
		JobID:        job.JobID,
		Owner:        job.LeaseOwner,
		Token:        job.LeaseToken,
		Attempt:      job.Attempts,
	}); err != nil {
		t.Fatal(err)
	}
}

func seedAcceptedObject(
	t *testing.T,
	repository *Repository,
	assignmentID string,
	acceptedAt time.Time,
) (ObjectAcceptance, ObjectPayloadStage) {
	t.Helper()
	ctx := context.Background()
	request := testReservationRequest(acceptedAt)
	request.AssignmentID = assignmentID
	request.IdempotencyKey = testID("operation")
	reservation, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	ciphertext := []byte("encrypted object deletion fixture")
	stage := ObjectPayloadStage{
		AssignmentID:     assignmentID,
		LedgerID:         reservation.Reservation.LedgerID,
		PayloadID:        testID("pay"),
		KeyReference:     "kms://regional-cell/key/delete",
		KeyVersion:       "delete-v1",
		Nonce:            []byte("delete-nonce"),
		CiphertextSHA256: SHA256(ciphertext),
		CiphertextSize:   int64(len(ciphertext)),
		ExpiresAt:        time.Now().UTC().Add(2 * time.Second),
		StagedAt:         acceptedAt,
	}
	if err := repository.StageObjectPayload(ctx, stage); err != nil {
		t.Fatal(err)
	}
	writeLease, ok, err := repository.ClaimObjectStageWrite(
		ctx,
		assignmentID,
		stage.LedgerID,
		"fixture-object-writer",
		testID("stage_token"),
		5*time.Minute,
	)
	if err != nil || !ok {
		t.Fatalf("failed to claim object fixture write: %#v %v %v", writeLease, ok, err)
	}
	if err := repository.FinalizeObjectPayload(ctx, writeLease, ObjectPromotion{
		ProviderStagingVersion: "fixture-stage-version",
		ProviderFinalVersion:   "fixture-final-version",
		Mode:                   "create_only",
	}); err != nil {
		t.Fatal(err)
	}
	acceptance := ObjectAcceptance{
		AssignmentID:        assignmentID,
		IdempotencyKey:      request.IdempotencyKey,
		LedgerID:            reservation.Reservation.LedgerID,
		IngestID:            testID("ing"),
		PayloadID:           stage.PayloadID,
		DeliveryID:          testID("del"),
		DispatchJobID:       testID("job"),
		EndpointSnapshotID:  putTestSnapshot(t, repository, assignmentID, acceptedAt),
		AcceptedAt:          acceptedAt,
		IdempotencyExpires:  acceptedAt.Add(24 * time.Hour),
		DispatchRunAt:       acceptedAt,
		DispatchMaxAttempts: 5,
	}
	if err := repository.AcceptFinalizedObject(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	return acceptance, stage
}

func expireJobLease(t *testing.T, pool *pgxpool.Pool, jobID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE durable_jobs
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE job_id = $1 AND state = 'leased'
	`, jobID); err != nil {
		t.Fatal(err)
	}
}

func expireOutboxLease(t *testing.T, pool *pgxpool.Pool, outboxID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE metadata_outbox
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE outbox_id = $1 AND state = 'leased'
	`, outboxID); err != nil {
		t.Fatal(err)
	}
}

func expirePayloadDeletionLease(t *testing.T, pool *pgxpool.Pool, payloadID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE payload_records
		SET deletion_lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE payload_id = $1 AND state = 'deleting'
	`, payloadID); err != nil {
		t.Fatal(err)
	}
}

func expireObjectStageLease(t *testing.T, pool *pgxpool.Pool, attemptID string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
		UPDATE payload_stage_write_attempts
		SET lease_expires_at = clock_timestamp() - interval '1 second'
		WHERE stage_write_attempt_id = $1 AND state = 'leased'
	`, attemptID); err != nil {
		t.Fatal(err)
	}
}

func waitForDatabaseTime(t *testing.T, pool *pgxpool.Pool, target time.Time) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for {
		var reached bool
		if err := pool.QueryRow(context.Background(), `
			SELECT clock_timestamp() >= $1
		`, target.UTC()).Scan(&reached); err != nil {
			t.Fatal(err)
		}
		if reached {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("database clock did not reach %s", target)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func newIntegrationRepository(t *testing.T) (*pgxpool.Pool, *Repository) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	schema := "hookship_test_" + randomHex(t, 8)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = identifier
	config.MaxConns = 12
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if err := Migrate(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	repository, err := New(pool)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	})
	return pool, repository
}

func testReservationRequest(now time.Time) ReservationRequest {
	return ReservationRequest{
		LedgerID:       testID("led"),
		AssignmentID:   testID("asg"),
		IdempotencyKey: testID("operation"),
		BodySHA256:     SHA256([]byte("raw event bytes")),
		Headers: StableHeaders{
			EventID:     "event-01",
			EventType:   "order.created",
			Route:       "orders.primary",
			ContentType: "application/octet-stream",
		},
		ReservedAt: now,
	}
}

func testPostgresAcceptance(
	assignmentID, idempotencyKey, ledgerID, snapshotID string,
	now time.Time,
) PostgresAcceptance {
	ciphertext := []byte("bounded encrypted PostgreSQL ciphertext")
	return PostgresAcceptance{
		AssignmentID:        assignmentID,
		IdempotencyKey:      idempotencyKey,
		LedgerID:            ledgerID,
		IngestID:            testID("ing"),
		PayloadID:           testID("pay"),
		DeliveryID:          testID("del"),
		DispatchJobID:       testID("job"),
		EndpointSnapshotID:  snapshotID,
		Ciphertext:          ciphertext,
		KeyReference:        "kms://regional-cell/key/7",
		KeyVersion:          "7",
		Nonce:               []byte("123456789012"),
		CiphertextSHA256:    SHA256(ciphertext),
		PayloadExpiresAt:    now.Add(24 * time.Hour),
		AcceptedAt:          now,
		IdempotencyExpires:  now.Add(24 * time.Hour),
		DispatchRunAt:       now,
		DispatchMaxAttempts: 5,
	}
}

func seedAcceptedPostgres(
	t *testing.T,
	repository *Repository,
	assignmentID string,
	now time.Time,
	maxAttempts int,
) PostgresAcceptance {
	t.Helper()
	ctx := context.Background()
	request := testReservationRequest(now)
	request.AssignmentID = assignmentID
	request.IdempotencyKey = testID("operation")
	reservation, err := repository.ReserveIdempotency(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	snapshotID := putTestSnapshot(t, repository, assignmentID, now)
	acceptance := testPostgresAcceptance(
		assignmentID,
		request.IdempotencyKey,
		reservation.Reservation.LedgerID,
		snapshotID,
		now,
	)
	acceptance.DispatchMaxAttempts = maxAttempts
	if err := repository.AcceptPostgres(ctx, acceptance); err != nil {
		t.Fatal(err)
	}
	return acceptance
}

func putTestSnapshot(
	t *testing.T,
	repository *Repository,
	assignmentID string,
	now time.Time,
) string {
	t.Helper()
	snapshotID := testID("snap")
	content := []byte(`{"endpoint":{"url":"https://example.test/hooks/orders","sealedSecret":{"keyReference":"kms://regional-cell/key/7","ciphertext":"ZW5jcnlwdGVk"}}}`)
	_, err := repository.PutEndpointSnapshot(context.Background(), EndpointSnapshot{
		SnapshotID:       snapshotID,
		AssignmentID:     assignmentID,
		EndpointID:       testID("endpoint"),
		BundleID:         testID("cfg"),
		ConfigGeneration: 7,
		Content:          content,
		ContentSHA256:    SHA256(content),
		CreatedAt:        now,
	})
	if err != nil {
		t.Fatal(err)
	}
	return snapshotID
}

func assertCount(
	t *testing.T,
	pool *pgxpool.Pool,
	query string,
	expected int,
	arguments ...any,
) {
	t.Helper()
	var count int
	if err := pool.QueryRow(context.Background(), query, arguments...).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != expected {
		t.Fatalf("expected count %d, got %d", expected, count)
	}
}

func testID(prefix string) string {
	var value [8]byte
	if _, err := rand.Read(value[:]); err != nil {
		panic(err)
	}
	return prefix + "_" + hex.EncodeToString(value[:])
}

func randomHex(t *testing.T, length int) string {
	t.Helper()
	value := make([]byte, length)
	if _, err := rand.Read(value); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(value)
}
