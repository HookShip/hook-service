package postgres

import (
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

func (r *outboxQueue) ClaimOutbox(
	ctx context.Context,
	assignmentID, owner, token string,
	leaseDuration time.Duration,
) (OutboxItem, bool, error) {
	for name, value := range map[string]string{
		"assignment ID": assignmentID,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return OutboxItem{}, false, err
		}
	}
	leaseMicroseconds, err := boundedDurationMicroseconds("outbox lease duration", leaseDuration, time.Hour, false)
	if err != nil {
		return OutboxItem{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return OutboxItem{}, false, fmt.Errorf("begin outbox claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := r.markExhaustedDead(ctx, tx, outboxTable, assignmentID, ""); err != nil {
		return OutboxItem{}, false, err
	}

	var item OutboxItem
	var leaseExpiry time.Time
	err = tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		),
		candidate AS (
			SELECT item.outbox_id
			FROM metadata_outbox AS item
			CROSS JOIN clock
			WHERE item.assignment_id = $1
			  AND item.attempts < item.max_attempts
			  AND item.run_at <= clock.now
			  AND (
				  item.state IN ('ready', 'retry')
				  OR (item.state = 'leased' AND item.lease_expires_at <= clock.now)
			  )
			ORDER BY item.run_at, item.created_at, item.outbox_id
			FOR UPDATE OF item SKIP LOCKED
			LIMIT 1
		)
		UPDATE metadata_outbox AS item
		SET
			state = 'leased',
			lease_owner = $2,
			lease_token = $3,
			lease_expires_at = clock.now + $4::bigint * interval '1 microsecond',
			attempts = item.attempts + 1,
			updated_at = clock.now
		FROM candidate, clock
		WHERE item.outbox_id = candidate.outbox_id
		RETURNING
			item.outbox_id,
			item.assignment_id,
			item.delivery_id,
			item.attempt_id,
			item.endpoint_snapshot_id,
			item.schema_version,
			item.event_kind,
			item.outcome,
			item.response_status,
			COALESCE(item.error_code, ''),
			item.occurred_at,
			item.run_at,
			item.max_attempts,
			item.state::text,
			item.lease_owner,
			item.lease_token,
			item.lease_expires_at,
			item.attempts,
			COALESCE(item.last_error_code, '')
	`, assignmentID, owner, token, leaseMicroseconds).Scan(
		&item.OutboxID,
		&item.AssignmentID,
		&item.DeliveryID,
		&item.AttemptID,
		&item.EndpointSnapshotID,
		&item.SchemaVersion,
		&item.EventKind,
		&item.Outcome,
		&item.ResponseStatus,
		&item.ErrorCode,
		&item.OccurredAt,
		&item.RunAt,
		&item.MaxAttempts,
		&item.State,
		&item.LeaseOwner,
		&item.LeaseToken,
		&leaseExpiry,
		&item.Attempts,
		&item.LastErrorCode,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		if err := tx.Commit(ctx); err != nil {
			return OutboxItem{}, false, fmt.Errorf("commit empty outbox claim: %w", err)
		}
		return OutboxItem{}, false, nil
	}
	if err != nil {
		return OutboxItem{}, false, fmt.Errorf("claim outbox row: %w", err)
	}
	item.LeaseExpiresAt = &leaseExpiry
	if err := tx.Commit(ctx); err != nil {
		return OutboxItem{}, false, fmt.Errorf("commit outbox claim: %w", err)
	}
	return item, true, nil
}

func (r *outboxQueue) PublishOutbox(
	ctx context.Context,
	lease OutboxLease,
) error {
	return r.settleLeased(ctx, outboxTable, "publish", setPublish, outboxLeaseKey(lease))
}

func (r *outboxQueue) RetryOutbox(
	ctx context.Context,
	lease OutboxLease,
	delay time.Duration,
	errorCode string,
) error {
	if errorCode != "" {
		if err := validateText("outbox error code", errorCode, 128); err != nil {
			return err
		}
	}
	delayMicroseconds, err := boundedDurationMicroseconds("outbox retry delay", delay, 30*24*time.Hour, true)
	if err != nil {
		return err
	}
	return r.settleLeased(ctx, outboxTable, "retry", setRetry, outboxLeaseKey(lease), delayMicroseconds, nullIfEmpty(errorCode))
}
