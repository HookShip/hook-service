package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

func (r *jobQueue) ClaimJob(
	ctx context.Context,
	assignmentID, kind, owner, token string,
	leaseDuration time.Duration,
) (Job, bool, error) {
	for name, value := range map[string]string{
		"assignment ID": assignmentID,
		"job kind":      kind,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return Job{}, false, err
		}
	}
	leaseMicroseconds, err := boundedDurationMicroseconds("job lease duration", leaseDuration, time.Hour, false)
	if err != nil {
		return Job{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return Job{}, false, fmt.Errorf("begin job claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.markExhaustedDead(ctx, tx, jobTable, assignmentID, kind); err != nil {
		return Job{}, false, err
	}

	var job Job
	var leaseExpiry time.Time
	err = tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		),
		candidate AS (
			SELECT job.job_id
			FROM durable_jobs AS job
			CROSS JOIN clock
			WHERE job.assignment_id = $1
			  AND job.kind = $2
			  AND job.attempts < job.max_attempts
			  AND job.run_at <= clock.now
			  AND (
				  job.state IN ('ready', 'retry')
				  OR (job.state = 'leased' AND job.lease_expires_at <= clock.now)
			  )
			ORDER BY job.run_at, job.created_at, job.job_id
			FOR UPDATE OF job SKIP LOCKED
			LIMIT 1
		)
		UPDATE durable_jobs AS job
		SET
			state = 'leased',
			lease_owner = $3,
			lease_token = $4,
			lease_expires_at = clock.now + $5::bigint * interval '1 microsecond',
			attempts = job.attempts + 1,
			updated_at = clock.now
		FROM candidate, clock
		WHERE job.job_id = candidate.job_id
		RETURNING
			job.job_id,
			job.assignment_id,
			job.kind,
			COALESCE(job.ingest_id, ''),
			COALESCE(job.payload_id, ''),
			COALESCE(job.delivery_id, ''),
			COALESCE(job.endpoint_snapshot_id, ''),
			job.state::text,
			job.run_at,
			job.lease_owner,
			job.lease_token,
			job.lease_expires_at,
			job.attempts,
			job.max_attempts,
			COALESCE(job.last_error_code, '')
	`, assignmentID, kind, owner, token, leaseMicroseconds).Scan(
		&job.JobID,
		&job.AssignmentID,
		&job.Kind,
		&job.IngestID,
		&job.PayloadID,
		&job.DeliveryID,
		&job.EndpointSnapshotID,
		&job.State,
		&job.RunAt,
		&job.LeaseOwner,
		&job.LeaseToken,
		&leaseExpiry,
		&job.Attempts,
		&job.MaxAttempts,
		&job.LastErrorCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return Job{}, false, fmt.Errorf("commit empty job claim: %w", err)
		}
		return Job{}, false, nil
	}
	if err != nil {
		return Job{}, false, fmt.Errorf("claim job: %w", err)
	}
	job.LeaseExpiresAt = &leaseExpiry
	if err := tx.Commit(ctx); err != nil {
		return Job{}, false, fmt.Errorf("commit job claim: %w", err)
	}
	return job, true, nil
}

func (r *jobQueue) CompleteJob(
	ctx context.Context,
	lease JobLease,
) error {
	return r.settleLeased(ctx, jobTable, "complete", setComplete, jobLeaseKey(lease))
}

func (r *jobQueue) RetryJob(
	ctx context.Context,
	lease JobLease,
	delay time.Duration,
	errorCode string,
) error {
	if errorCode != "" {
		if err := validateText("job error code", errorCode, 128); err != nil {
			return err
		}
	}
	delayMicroseconds, err := boundedDurationMicroseconds("job retry delay", delay, 30*24*time.Hour, true)
	if err != nil {
		return err
	}
	return r.settleLeased(ctx, jobTable, "retry", setRetry, jobLeaseKey(lease), delayMicroseconds, nullIfEmpty(errorCode))
}

// FailJob terminally dead-letters a leased job regardless of remaining attempts.
// It is used for non-retryable (terminal policy) delivery outcomes and requires
// the caller to still hold the fencing lease.
func (r *jobQueue) FailJob(
	ctx context.Context,
	lease JobLease,
	errorCode string,
) error {
	if errorCode != "" {
		if err := validateText("job error code", errorCode, 128); err != nil {
			return err
		}
	}
	return r.settleLeased(ctx, jobTable, "fail", setFail, jobLeaseKey(lease), nullIfEmpty(errorCode))
}

func (r *jobQueue) RecordAttemptAndOutbox(
	ctx context.Context,
	attempt AttemptRecord,
	outbox OutboxRecord,
) error {
	if attempt.AssignmentID != outbox.AssignmentID ||
		attempt.AttemptID != outbox.AttemptID ||
		attempt.DeliveryID != outbox.DeliveryID ||
		attempt.EndpointSnapshotID != outbox.EndpointSnapshotID {
		return errors.New("attempt and outbox lineage must match")
	}
	for name, value := range map[string]string{
		"attempt ID":           attempt.AttemptID,
		"assignment ID":        attempt.AssignmentID,
		"delivery ID":          attempt.DeliveryID,
		"job ID":               attempt.JobID,
		"endpoint snapshot ID": attempt.EndpointSnapshotID,
		"attempt outcome":      attempt.Outcome,
		"outbox ID":            outbox.OutboxID,
		"schema version":       outbox.SchemaVersion,
		"event kind":           outbox.EventKind,
		"outbox outcome":       outbox.Outcome,
	} {
		if err := validateText(name, value, 255); err != nil {
			return err
		}
	}
	if attempt.AttemptNumber < 1 || attempt.AttemptNumber > 1000 {
		return errors.New("attempt number must be between 1 and 1000")
	}
	if attempt.DurationMS < 0 || attempt.DurationMS > 3600000 {
		return errors.New("attempt duration is out of range")
	}
	if attempt.StartedAt.IsZero() || attempt.FinishedAt.Before(attempt.StartedAt) {
		return errors.New("attempt timestamps are invalid")
	}
	if outbox.MaxAttempts < 1 || outbox.MaxAttempts > 1000 {
		return errors.New("outbox max attempts must be between 1 and 1000")
	}
	if outbox.OccurredAt.IsZero() {
		outbox.OccurredAt = attempt.FinishedAt
	}
	if outbox.RunAt.IsZero() {
		outbox.RunAt = outbox.OccurredAt
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin attempt and outbox write: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if _, err := tx.Exec(ctx, `
		INSERT INTO delivery_attempts (
			attempt_id,
			assignment_id,
			delivery_id,
			job_id,
			endpoint_snapshot_id,
			attempt_number,
			outcome,
			response_status,
			error_code,
			duration_ms,
			started_at,
			finished_at,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
	`,
		attempt.AttemptID,
		attempt.AssignmentID,
		attempt.DeliveryID,
		attempt.JobID,
		attempt.EndpointSnapshotID,
		attempt.AttemptNumber,
		attempt.Outcome,
		attempt.ResponseStatus,
		nullIfEmpty(attempt.ErrorCode),
		attempt.DurationMS,
		attempt.StartedAt.UTC(),
		attempt.FinishedAt.UTC(),
	); err != nil {
		return fmt.Errorf("insert immutable delivery attempt: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO metadata_outbox (
			outbox_id,
			assignment_id,
			delivery_id,
			attempt_id,
			endpoint_snapshot_id,
			schema_version,
			event_kind,
			outcome,
			response_status,
			error_code,
			occurred_at,
			state,
			run_at,
			attempts,
			max_attempts,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, 'ready', $12, 0, $13, $11, $11)
	`,
		outbox.OutboxID,
		outbox.AssignmentID,
		outbox.DeliveryID,
		outbox.AttemptID,
		outbox.EndpointSnapshotID,
		outbox.SchemaVersion,
		outbox.EventKind,
		outbox.Outcome,
		outbox.ResponseStatus,
		nullIfEmpty(outbox.ErrorCode),
		outbox.OccurredAt.UTC(),
		outbox.RunAt.UTC(),
		outbox.MaxAttempts,
	); err != nil {
		return fmt.Errorf("insert payload-free metadata outbox: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit attempt and outbox write: %w", err)
	}
	return nil
}
