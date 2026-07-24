package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// This file holds the object-stage write state-machine steps shared by
// ClaimObjectStageWrite and SupersedeObjectStage. Each helper runs inside a
// transaction owned by its caller (the orchestration method begins, commits, or
// rolls back), operating on the payload_stage_write_attempts and payload_records
// tables. Keeping the steps here keeps the orchestration methods short and makes
// the fencing and single-winner invariants reviewable in isolation.

// leasedAttempt carries the identity of a new leased stage-write attempt to
// insert. The checksum and size are supplied explicitly (rather than read from
// the payload row) so a supersession can install a fresh ciphertext identity in
// the same statement that leases its attempt.
type leasedAttempt struct {
	assignmentID string
	ledgerID     string
	attemptID    string
	attempt      int
	stagingKey   string
	finalKey     string
	checksum     []byte
	size         int64
	owner        string
	token        string
	leaseMicros  int64
}

// stageAttemptKeys derives the deterministic identity of a stage-write attempt:
// its attempt ID and its immutable staging and final object keys.
func stageAttemptKeys(assignmentID, payloadID string, attempt int) (attemptID, stagingKey, finalKey string, err error) {
	attemptID = stageWriteAttemptID(assignmentID, payloadID, attempt)
	stagingKey, finalKey, err = ObjectAttemptKeys(assignmentID, payloadID, attempt)
	return attemptID, stagingKey, finalKey, err
}

// claimReadyAttempt leases a stage-write attempt that is already in the `ready`
// state, using the database clock for the lease deadline. It returns
// ErrPayloadUnavailable if the attempt is no longer ready or its payload is no
// longer an unexpired `staging` row.
func (r *objectStageStore) claimReadyAttempt(
	ctx context.Context, tx pgx.Tx, assignmentID, attemptID, owner, token string, leaseMicros int64,
) (time.Time, error) {
	var leaseExpiresAt time.Time
	err := tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE payload_stage_write_attempts
		SET
			state = 'leased',
			lease_owner = $3,
			lease_token = $4,
			lease_expires_at = clock.now + $5::bigint * interval '1 microsecond'
		FROM clock, payload_records AS payload
		WHERE payload_stage_write_attempts.assignment_id = $1
		  AND payload_stage_write_attempts.stage_write_attempt_id = $2
		  AND payload_stage_write_attempts.state = 'ready'
		  AND payload.assignment_id = payload_stage_write_attempts.assignment_id
		  AND payload.payload_id = payload_stage_write_attempts.payload_id
		  AND payload.state = 'staging'
		  AND payload.expires_at > clock.now
		RETURNING payload_stage_write_attempts.lease_expires_at
	`, assignmentID, attemptID, owner, token, leaseMicros).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPayloadUnavailable
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("claim ready object stage write: %w", err)
	}
	return leaseExpiresAt, nil
}

// orphanExpiredLeasedAttempt reclaims one specific stale attempt whose lease has
// expired, moving it to `orphaned` so its objects become cleanup-owned. It is a
// no-op if the attempt is not an expired lease.
func (r *objectStageStore) orphanExpiredLeasedAttempt(ctx context.Context, tx pgx.Tx, assignmentID, attemptID string) error {
	if _, err := tx.Exec(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE payload_stage_write_attempts
		SET
			state = 'orphaned',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			orphaned_at = clock.now
		FROM clock
		WHERE assignment_id = $1
		  AND stage_write_attempt_id = $2
		  AND state = 'leased'
		  AND lease_expires_at <= clock.now
	`, assignmentID, attemptID); err != nil {
		return fmt.Errorf("orphan stale object stage write: %w", err)
	}
	return nil
}

// fencePriorAttempts orphans every prior ready or leased attempt for a payload
// so that no stale writer can ever finalize after a supersession.
func (r *objectStageStore) fencePriorAttempts(ctx context.Context, tx pgx.Tx, assignmentID, payloadID string) error {
	if _, err := tx.Exec(ctx, `
		WITH clock AS MATERIALIZED (SELECT clock_timestamp() AS now)
		UPDATE payload_stage_write_attempts AS attempt
		SET state = 'orphaned', lease_owner = NULL, lease_token = NULL,
			lease_expires_at = NULL, orphaned_at = clock.now
		FROM clock
		WHERE attempt.assignment_id = $1
		  AND attempt.payload_id = $2
		  AND attempt.state IN ('ready', 'leased')
	`, assignmentID, payloadID); err != nil {
		return fmt.Errorf("fence prior object stage attempts: %w", err)
	}
	return nil
}

// hasActiveWriter reports whether any stage-write attempt for the payload still
// holds an unexpired lease. It is the single-winner fence for supersession.
func (r *objectStageStore) hasActiveWriter(ctx context.Context, tx pgx.Tx, assignmentID, payloadID string) (bool, error) {
	var active bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM payload_stage_write_attempts
			WHERE assignment_id = $1
			  AND payload_id = $2
			  AND state = 'leased'
			  AND lease_expires_at > clock_timestamp()
		)
	`, assignmentID, payloadID).Scan(&active); err != nil {
		return false, fmt.Errorf("check active object stage writer: %w", err)
	}
	return active, nil
}

// maxAttemptNumber returns the highest attempt number recorded for a payload, or
// zero when none exist, so the caller can allocate the next attempt.
func (r *objectStageStore) maxAttemptNumber(ctx context.Context, tx pgx.Tx, assignmentID, payloadID string) (int, error) {
	var maxAttempt int
	if err := tx.QueryRow(ctx, `
		SELECT COALESCE(MAX(attempt_number), 0)
		FROM payload_stage_write_attempts
		WHERE assignment_id = $1 AND payload_id = $2
	`, assignmentID, payloadID).Scan(&maxAttempt); err != nil {
		return 0, fmt.Errorf("read prior object stage attempts: %w", err)
	}
	return maxAttempt, nil
}

// moveStagedIdentityKeys repoints a still-`staging` payload row at a new
// attempt's staging and final object keys (used when a claim leases a fresh
// replacement attempt).
func (r *objectStageStore) moveStagedIdentityKeys(ctx context.Context, tx pgx.Tx, assignmentID, ledgerID, stagingKey, finalKey string) error {
	if _, err := tx.Exec(ctx, `
		UPDATE payload_records
		SET staging_object_key = $3, final_object_key = $4
		WHERE assignment_id = $1
		  AND ledger_id = $2
		  AND state = 'staging'
	`, assignmentID, ledgerID, stagingKey, finalKey); err != nil {
		return fmt.Errorf("select replacement object stage identity: %w", err)
	}
	return nil
}

// insertLeasedAttempt inserts a new leased stage-write attempt, guarded so the
// insert only succeeds while the payload is still an unexpired `staging` row.
// It returns the database-clock lease deadline, or ErrPayloadUnavailable when
// the payload guard fails. Both the claim replacement and the supersession use
// this single guarded insert so the leased-attempt invariant is defined once.
func (r *objectStageStore) insertLeasedAttempt(ctx context.Context, tx pgx.Tx, a leasedAttempt) (time.Time, error) {
	var leaseExpiresAt time.Time
	err := tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		INSERT INTO payload_stage_write_attempts (
			stage_write_attempt_id,
			assignment_id,
			payload_id,
			ledger_id,
			attempt_number,
			state,
			staging_object_key,
			final_object_key,
			ciphertext_sha256,
			ciphertext_size,
			lease_owner,
			lease_token,
			lease_expires_at,
			created_at
		)
		SELECT
			$3,
			payload.assignment_id,
			payload.payload_id,
			payload.ledger_id,
			$4,
			'leased',
			$5,
			$6,
			$7,
			$8,
			$9,
			$10,
			clock.now + $11::bigint * interval '1 microsecond',
			clock.now
		FROM payload_records AS payload
		CROSS JOIN clock
		WHERE payload.assignment_id = $1
		  AND payload.ledger_id = $2
		  AND payload.state = 'staging'
		  AND payload.expires_at > clock.now
		RETURNING lease_expires_at
	`,
		a.assignmentID,
		a.ledgerID,
		a.attemptID,
		a.attempt,
		a.stagingKey,
		a.finalKey,
		a.checksum,
		a.size,
		a.owner,
		a.token,
		a.leaseMicros,
	).Scan(&leaseExpiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrPayloadUnavailable
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("insert leased object stage attempt: %w", err)
	}
	return leaseExpiresAt, nil
}
