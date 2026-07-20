package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// PayloadCiphertext is the committed encrypted payload plus the metadata needed
// to decrypt it inside a worker operation.
type PayloadCiphertext struct {
	Ciphertext       []byte
	Nonce            []byte
	KeyReference     string
	KeyVersion       string
	CiphertextSHA256 Hash
}

// IngestStatusRow is the payload-free status of a committed ingest.
type IngestStatusRow struct {
	EventID              string
	EventType            string
	Route                string
	ContentType          string
	BodySHA256           Hash
	AcceptedAt           time.Time
	IdempotencyExpiresAt time.Time
	UpdatedAt            time.Time
	DispatchState        string
}

// ReserveNonce records a first-seen (credentialID, nonce) for replay protection.
// It returns true when the nonce is fresh (or the previous record has expired
// and is refreshed) and false when an unexpired nonce is replayed. The window is
// enforced with the database clock to avoid client skew.
func (r *Repository) ReserveNonce(
	ctx context.Context,
	credentialID, nonce string,
	signedAt, expiresAt time.Time,
) (bool, error) {
	if err := validateText("credential ID", credentialID, 255); err != nil {
		return false, err
	}
	if err := validateText("nonce", nonce, 512); err != nil {
		return false, err
	}
	if signedAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(signedAt) {
		return false, errors.New("nonce expiry must be after the signed timestamp")
	}
	signedAt = signedAt.UTC().Truncate(time.Microsecond)
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return false, fmt.Errorf("begin nonce reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var active bool
	err = tx.QueryRow(ctx, `
		SELECT expires_at > clock_timestamp()
		FROM nonce_ledger
		WHERE credential_id = $1 AND nonce = $2
		FOR UPDATE
	`, credentialID, nonce).Scan(&active)
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		if _, err := tx.Exec(ctx, `
			INSERT INTO nonce_ledger (credential_id, nonce, signed_at, expires_at, created_at)
			VALUES ($1, $2, $3, $4, clock_timestamp())
		`, credentialID, nonce, signedAt, expiresAt); err != nil {
			return false, fmt.Errorf("insert nonce: %w", err)
		}
	case err != nil:
		return false, fmt.Errorf("read nonce: %w", err)
	case active:
		if err := tx.Commit(ctx); err != nil {
			return false, fmt.Errorf("commit nonce replay: %w", err)
		}
		return false, nil
	default:
		// The prior nonce expired; refresh it under the new window.
		if _, err := tx.Exec(ctx, `
			UPDATE nonce_ledger
			SET signed_at = $3, expires_at = $4, created_at = clock_timestamp()
			WHERE credential_id = $1 AND nonce = $2
		`, credentialID, nonce, signedAt, expiresAt); err != nil {
			return false, fmt.Errorf("refresh expired nonce: %w", err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return false, fmt.Errorf("commit nonce reservation: %w", err)
	}
	return true, nil
}

// PurgeExpiredNonces removes nonce rows past their retention window.
func (r *Repository) PurgeExpiredNonces(ctx context.Context, limit int) (int64, error) {
	if limit < 1 || limit > 100000 {
		return 0, errors.New("nonce purge limit must be between 1 and 100000")
	}
	tag, err := r.pool.Exec(ctx, `
		WITH expired AS (
			SELECT credential_id, nonce
			FROM nonce_ledger
			WHERE expires_at <= clock_timestamp()
			ORDER BY expires_at
			LIMIT $1::integer
		)
		DELETE FROM nonce_ledger AS ledger
		USING expired
		WHERE ledger.credential_id = expired.credential_id
		  AND ledger.nonce = expired.nonce
	`, limit)
	if err != nil {
		return 0, fmt.Errorf("purge expired nonces: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListDeadJobs returns terminal dead dispatch jobs for an assignment, for
// operator diagnostics.
func (r *Repository) ListDeadJobs(
	ctx context.Context,
	assignmentID string,
	limit int,
) ([]Job, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("dead job limit must be between 1 and 10000")
	}
	rows, err := r.pool.Query(ctx, `
		SELECT
			job_id,
			assignment_id,
			kind,
			COALESCE(ingest_id, ''),
			COALESCE(payload_id, ''),
			COALESCE(delivery_id, ''),
			COALESCE(endpoint_snapshot_id, ''),
			state::text,
			run_at,
			attempts,
			max_attempts,
			COALESCE(last_error_code, '')
		FROM durable_jobs
		WHERE assignment_id = $1 AND state = 'dead'
		ORDER BY completed_at DESC NULLS LAST, job_id
		LIMIT $2::integer
	`, assignmentID, limit)
	if err != nil {
		return nil, fmt.Errorf("list dead jobs: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		if err := rows.Scan(
			&job.JobID,
			&job.AssignmentID,
			&job.Kind,
			&job.IngestID,
			&job.PayloadID,
			&job.DeliveryID,
			&job.EndpointSnapshotID,
			&job.State,
			&job.RunAt,
			&job.Attempts,
			&job.MaxAttempts,
			&job.LastErrorCode,
		); err != nil {
			return nil, fmt.Errorf("scan dead job: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate dead jobs: %w", err)
	}
	return jobs, nil
}

// RedriveJob returns a dead dispatch job to the ready state as an explicit
// operator action, resetting the attempt counter. It never silently mutates a
// live job.
func (r *Repository) RedriveJob(
	ctx context.Context,
	assignmentID, jobID string,
	runAt time.Time,
) error {
	if runAt.IsZero() {
		runAt = time.Now().UTC()
	}
	tag, err := r.pool.Exec(ctx, `
		UPDATE durable_jobs
		SET
			state = 'ready',
			attempts = 0,
			run_at = $3,
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			last_error_code = NULL,
			completed_at = NULL,
			updated_at = clock_timestamp()
		WHERE assignment_id = $1 AND job_id = $2 AND state = 'dead'
	`, assignmentID, jobID, runAt.UTC())
	if err != nil {
		return fmt.Errorf("redrive dead job: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotFound
	}
	return nil
}

func (r *Repository) LoadPayloadCiphertext(
	ctx context.Context,
	assignmentID, payloadID string,
) (PayloadCiphertext, error) {
	var payload PayloadCiphertext
	var checksum []byte
	err := r.pool.QueryRow(ctx, `
		SELECT ciphertext, nonce, key_reference, key_version, ciphertext_sha256
		FROM payload_records
		WHERE assignment_id = $1 AND payload_id = $2 AND backend = 'postgres' AND state = 'committed'
	`, assignmentID, payloadID).Scan(
		&payload.Ciphertext,
		&payload.Nonce,
		&payload.KeyReference,
		&payload.KeyVersion,
		&checksum,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return PayloadCiphertext{}, ErrNotFound
	}
	if err != nil {
		return PayloadCiphertext{}, fmt.Errorf("load payload ciphertext: %w", err)
	}
	copy(payload.CiphertextSHA256[:], checksum)
	return payload, nil
}

// LoadIngestStatus reads the payload-free status of an ingest owned by the
// assignment. Unknown and foreign IDs both return ErrNotFound.
func (r *Repository) LoadIngestStatus(
	ctx context.Context,
	assignmentID, ingestID string,
) (IngestStatusRow, error) {
	var row IngestStatusRow
	var bodyHash []byte
	err := r.pool.QueryRow(ctx, `
		SELECT
			ingest.event_id,
			ingest.event_type,
			ingest.route,
			ingest.content_type,
			ingest.body_sha256,
			ingest.accepted_at,
			ingest.idempotency_expires_at,
			COALESCE(job.updated_at, ingest.accepted_at),
			COALESCE(job.state::text, 'unknown')
		FROM event_ingests AS ingest
		LEFT JOIN durable_jobs AS job
			ON job.assignment_id = ingest.assignment_id
			AND job.ingest_id = ingest.ingest_id
			AND job.kind = 'dispatch'
		WHERE ingest.assignment_id = $1 AND ingest.ingest_id = $2
	`, assignmentID, ingestID).Scan(
		&row.EventID,
		&row.EventType,
		&row.Route,
		&row.ContentType,
		&bodyHash,
		&row.AcceptedAt,
		&row.IdempotencyExpiresAt,
		&row.UpdatedAt,
		&row.DispatchState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return IngestStatusRow{}, ErrNotFound
	}
	if err != nil {
		return IngestStatusRow{}, fmt.Errorf("load ingest status: %w", err)
	}
	copy(row.BodySHA256[:], bodyHash)
	return row, nil
}

// ListReadyDispatch returns dispatch jobs that are currently claimable, for the
// relay to publish advisory dispatch signals. It never mutates state, so
// publishing signals derived from it is not a database-plus-queue dual write.
func (r *Repository) ListReadyDispatch(ctx context.Context, limit int) ([]Job, error) {
	if limit < 1 || limit > 10000 {
		return nil, errors.New("ready dispatch limit must be between 1 and 10000")
	}
	rows, err := r.pool.Query(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		SELECT
			job.job_id,
			job.assignment_id,
			job.kind,
			COALESCE(job.ingest_id, ''),
			COALESCE(job.payload_id, ''),
			COALESCE(job.delivery_id, ''),
			COALESCE(job.endpoint_snapshot_id, ''),
			job.state::text,
			job.run_at,
			job.attempts,
			job.max_attempts,
			COALESCE(job.last_error_code, '')
		FROM durable_jobs AS job
		CROSS JOIN clock
		WHERE job.kind = 'dispatch'
		  AND job.attempts < job.max_attempts
		  AND job.run_at <= clock.now
		  AND (
			  job.state IN ('ready', 'retry')
			  OR (job.state = 'leased' AND job.lease_expires_at <= clock.now)
		  )
		ORDER BY job.run_at, job.created_at, job.job_id
		LIMIT $1::integer
	`, limit)
	if err != nil {
		return nil, fmt.Errorf("list ready dispatch: %w", err)
	}
	defer rows.Close()

	var jobs []Job
	for rows.Next() {
		var job Job
		if err := rows.Scan(
			&job.JobID,
			&job.AssignmentID,
			&job.Kind,
			&job.IngestID,
			&job.PayloadID,
			&job.DeliveryID,
			&job.EndpointSnapshotID,
			&job.State,
			&job.RunAt,
			&job.Attempts,
			&job.MaxAttempts,
			&job.LastErrorCode,
		); err != nil {
			return nil, fmt.Errorf("scan ready dispatch: %w", err)
		}
		jobs = append(jobs, job)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate ready dispatch: %w", err)
	}
	return jobs, nil
}
