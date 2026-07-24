package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

func (r *retentionManager) ListPendingReservations(
	ctx context.Context,
	assignmentID string,
	olderThan time.Time,
	limit int,
) ([]Reservation, error) {
	if limit < 1 || limit > 1000 {
		return nil, errors.New("pending reservation limit must be between 1 and 1000")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
			ledger_id,
			assignment_id,
			idempotency_key,
			request_fingerprint,
			body_sha256,
			event_id,
			event_type,
			route,
			content_type,
			state::text,
			COALESCE(ingest_id, ''),
			COALESCE(payload_id, ''),
			COALESCE(dispatch_job_id, ''),
			reserved_at,
			accepted_at,
			idempotency_expires_at,
			tombstone_expires_at
		FROM ingest_idempotency_ledgers
		WHERE assignment_id = $1
		  AND state = 'pending'
		  AND reserved_at <= $2
		ORDER BY reserved_at, ledger_id
		LIMIT $3::integer
	`, assignmentID, olderThan.UTC(), limit)
	if err != nil {
		return nil, fmt.Errorf("list pending reservations: %w", err)
	}
	defer rows.Close()

	var result []Reservation
	for rows.Next() {
		reservation, err := scanReservation(rows)
		if err != nil {
			return nil, fmt.Errorf("scan pending reservation: %w", err)
		}
		result = append(result, reservation)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate pending reservations: %w", err)
	}
	return result, nil
}

func (r *retentionManager) AdvanceTombstones(
	ctx context.Context,
	assignmentID string,
	limit int,
) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, errors.New("tombstone transition limit must be between 1 and 10000")
	}
	tag, err := r.pool.Exec(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		),
		candidates AS (
			SELECT ledger.ledger_id
			FROM ingest_idempotency_ledgers AS ledger
			CROSS JOIN clock
			WHERE ledger.assignment_id = $1
			  AND ledger.state = 'committed'
			  AND ledger.idempotency_expires_at <= clock.now
			ORDER BY ledger.idempotency_expires_at, ledger.ledger_id
			FOR UPDATE OF ledger SKIP LOCKED
			LIMIT $2::integer
		)
		UPDATE ingest_idempotency_ledgers AS ledger
		SET state = 'tombstone', updated_at = clock.now
		FROM candidates, clock
		WHERE ledger.ledger_id = candidates.ledger_id
	`, assignmentID, limit)
	if err != nil {
		return 0, fmt.Errorf("advance idempotency tombstones: %w", err)
	}
	return tag.RowsAffected(), nil
}

// PurgeTerminalOutbox deletes metadata_outbox rows that have reached a terminal
// state (published or dead) and whose completion is older than the retention
// horizon. The outbox is a leaf table, so bounded deletion is FK-safe. It caps
// unbounded growth of the payload-free metadata outbox.
func (r *retentionManager) PurgeTerminalOutbox(
	ctx context.Context,
	assignmentID string,
	olderThan time.Time,
	limit int,
) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, errors.New("outbox purge limit must be between 1 and 10000")
	}
	tag, err := r.pool.Exec(ctx, `
		WITH candidates AS (
			SELECT outbox_id
			FROM metadata_outbox
			WHERE assignment_id = $1
			  AND state IN ('published', 'dead')
			  AND completed_at IS NOT NULL
			  AND completed_at <= $2
			ORDER BY completed_at, outbox_id
			FOR UPDATE SKIP LOCKED
			LIMIT $3::integer
		)
		DELETE FROM metadata_outbox AS outbox
		USING candidates
		WHERE outbox.outbox_id = candidates.outbox_id
	`, assignmentID, olderThan.UTC(), limit)
	if err != nil {
		return 0, fmt.Errorf("purge terminal outbox: %w", err)
	}
	return tag.RowsAffected(), nil
}

func (r *retentionManager) MarkExpiredObjectStagesOrphaned(
	ctx context.Context,
	assignmentID string,
	limit int,
) (int64, error) {
	if limit < 1 || limit > 10000 {
		return 0, errors.New("orphan transition limit must be between 1 and 10000")
	}
	tag, err := r.pool.Exec(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		),
		candidates AS MATERIALIZED (
			SELECT payload.payload_id
			FROM payload_records AS payload
			JOIN ingest_idempotency_ledgers AS ledger
			  ON ledger.assignment_id = payload.assignment_id
			 AND ledger.ledger_id = payload.ledger_id
			CROSS JOIN clock
			WHERE payload.assignment_id = $1
			  AND payload.backend = 'object_store'
			  AND payload.state IN ('staging', 'finalized')
			  AND payload.expires_at <= clock.now
			  AND ledger.state = 'pending'
			  AND NOT EXISTS (
				  SELECT 1
				  FROM payload_stage_write_attempts AS attempt
				  WHERE attempt.assignment_id = payload.assignment_id
				    AND attempt.payload_id = payload.payload_id
				    AND attempt.state = 'leased'
				    AND attempt.lease_expires_at > clock.now
			  )
			ORDER BY payload.expires_at, payload.payload_id
			FOR UPDATE OF payload SKIP LOCKED
			LIMIT $2::integer
		),
		orphan_attempts AS (
			UPDATE payload_stage_write_attempts AS attempt
			SET
				state = 'orphaned',
				lease_owner = NULL,
				lease_token = NULL,
				lease_expires_at = NULL,
				orphaned_at = clock.now
			FROM candidates, clock
			WHERE attempt.assignment_id = $1
			  AND attempt.payload_id = candidates.payload_id
			  AND (
				  attempt.state = 'ready'
				  OR (
					  attempt.state = 'leased'
					  AND attempt.lease_expires_at <= clock.now
				  )
			  )
			RETURNING attempt.stage_write_attempt_id
		)
		UPDATE payload_records AS payload
		SET state = 'orphaned'
		FROM candidates
		WHERE payload.payload_id = candidates.payload_id
	`, assignmentID, limit)
	if err != nil {
		return 0, fmt.Errorf("mark object payload stages orphaned: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ClaimPayloadDeletion claims or reclaims one eligible payload using the
// PostgreSQL clock and returns the immutable metadata needed for deletion.
func (r *retentionManager) ClaimPayloadDeletion(
	ctx context.Context,
	assignmentID, owner, token string,
	leaseDuration time.Duration,
) (PayloadDeletionLease, bool, error) {
	for name, value := range map[string]string{
		"assignment ID":        assignmentID,
		"deletion lease owner": owner,
		"deletion lease token": token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return PayloadDeletionLease{}, false, err
		}
	}
	leaseMicroseconds, err := boundedDurationMicroseconds(
		"payload deletion lease duration",
		leaseDuration,
		time.Hour,
		false,
	)
	if err != nil {
		return PayloadDeletionLease{}, false, err
	}

	var lease PayloadDeletionLease
	var checksum []byte
	err = r.pool.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		),
		candidate AS (
			SELECT payload.payload_id
			FROM payload_records AS payload
			JOIN ingest_idempotency_ledgers AS ledger
			  ON ledger.assignment_id = payload.assignment_id
			 AND ledger.ledger_id = payload.ledger_id
			CROSS JOIN clock
			WHERE payload.assignment_id = $1
			  AND NOT EXISTS (
				  SELECT 1
				  FROM durable_jobs AS job
				  WHERE job.assignment_id = payload.assignment_id
				    AND job.payload_id = payload.payload_id
				    AND job.kind <> 'payload_delete'
				    AND job.state IN ('ready', 'leased', 'retry')
			  )
			  AND NOT EXISTS (
				  SELECT 1
				  FROM payload_stage_write_attempts AS attempt
				  WHERE attempt.assignment_id = payload.assignment_id
				    AND attempt.payload_id = payload.payload_id
				    AND attempt.state = 'leased'
				    AND attempt.lease_expires_at > clock.now
			  )
			  AND (
				  (
					  payload.state = 'deleting'
					  AND payload.deletion_lease_expires_at <= clock.now
				  )
				  OR (
					  payload.expires_at <= clock.now
					  AND (
						  payload.state = 'orphaned'
						  OR (
							  payload.state = 'committed'
							  AND ledger.state = 'tombstone'
						  )
					  )
				  )
			  )
			ORDER BY
				COALESCE(payload.deletion_lease_expires_at, payload.expires_at),
				payload.payload_id
			FOR UPDATE OF payload SKIP LOCKED
			LIMIT 1
		)
		UPDATE payload_records AS payload
		SET
			state = 'deleting',
			deletion_started_at = COALESCE(payload.deletion_started_at, clock.now),
			deletion_lease_owner = $2,
			deletion_lease_token = $3,
			deletion_lease_expires_at = clock.now + $4::bigint * interval '1 microsecond',
			deletion_attempts = payload.deletion_attempts + 1
		FROM candidate, clock
		WHERE payload.payload_id = candidate.payload_id
		RETURNING
			payload.assignment_id,
			payload.payload_id,
			payload.backend::text,
			payload.key_reference,
			payload.key_version,
			payload.ciphertext_sha256,
			payload.ciphertext_size,
			COALESCE(payload.staging_object_key, ''),
			COALESCE(payload.final_object_key, ''),
			payload.deletion_lease_owner,
			payload.deletion_lease_token,
			payload.deletion_attempts,
			payload.deletion_started_at,
			payload.deletion_lease_expires_at
	`, assignmentID, owner, token, leaseMicroseconds).Scan(
		&lease.AssignmentID,
		&lease.PayloadID,
		&lease.Backend,
		&lease.KeyReference,
		&lease.KeyVersion,
		&checksum,
		&lease.CiphertextSize,
		&lease.StagingObjectKey,
		&lease.FinalObjectKey,
		&lease.Owner,
		&lease.Token,
		&lease.Attempt,
		&lease.StartedAt,
		&lease.ExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PayloadDeletionLease{}, false, nil
	}
	if err != nil {
		return PayloadDeletionLease{}, false, fmt.Errorf("claim payload deletion: %w", err)
	}
	if len(checksum) != len(lease.CiphertextSHA256) {
		return PayloadDeletionLease{}, false, errors.New("stored payload deletion checksum has invalid length")
	}
	copy(lease.CiphertextSHA256[:], checksum)
	return lease, true, nil
}

// CompletePayloadDeletion records a successful ciphertext deletion only while
// the caller still owns the unexpired database lease.
func (r *retentionManager) CompletePayloadDeletion(
	ctx context.Context,
	lease PayloadDeletionLease,
) error {
	tag, err := r.pool.Exec(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE payload_records
		SET
			state = 'deleted',
			ciphertext = NULL,
			deleted_at = clock.now,
			deletion_lease_owner = NULL,
			deletion_lease_token = NULL,
			deletion_lease_expires_at = NULL
		FROM clock
		WHERE assignment_id = $1
		  AND payload_id = $2
		  AND state = 'deleting'
		  AND deletion_lease_owner = $3
		  AND deletion_lease_token = $4
		  AND deletion_attempts = $5
		  AND deletion_lease_expires_at > clock.now
	`, lease.AssignmentID, lease.PayloadID, lease.Owner, lease.Token, lease.Attempt)
	if err != nil {
		return fmt.Errorf("complete payload deletion: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
