package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// FinalizeObjectPayload records a conditionally promoted provider object only
// while the stage-write lease is still current, then marks the payload
// finalized. Both writes are fenced by the lease.
func (r *objectStageStore) FinalizeObjectPayload(
	ctx context.Context,
	lease ObjectStageWriteLease,
	promotion ObjectPromotion,
) error {
	if err := validatePromotion(promotion); err != nil {
		return err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object payload finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	promotedAt, err := fencePromotedAttempt(ctx, tx, lease, promotion)
	if err != nil {
		return err
	}
	if err := finalizePayloadRecord(ctx, tx, lease, promotedAt); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit object payload finalization: %w", err)
	}
	return nil
}

// validatePromotion checks the provider versions and promotion-mode invariants.
func validatePromotion(promotion ObjectPromotion) error {
	for name, value := range map[string]string{
		"provider staging version": promotion.ProviderStagingVersion,
		"provider final version":   promotion.ProviderFinalVersion,
	} {
		if err := validateText(name, value, 512); err != nil {
			return err
		}
	}
	switch promotion.Mode {
	case "create_only", "verified_existing":
		if promotion.ExpectedFinalVersion != "" {
			return errors.New("expected final version is forbidden for this promotion mode")
		}
	case "match_version":
		if err := validateText("expected final version", promotion.ExpectedFinalVersion, 512); err != nil {
			return err
		}
	default:
		return errors.New("promotion mode must be create_only, match_version, or verified_existing")
	}
	return nil
}

// fencePromotedAttempt records the provider promotion on the stage-write attempt
// only while the caller still holds the exact unexpired lease and the payload is
// still an unexpired staging row. It returns ErrLeaseLost otherwise.
func fencePromotedAttempt(ctx context.Context, tx pgx.Tx, lease ObjectStageWriteLease, promotion ObjectPromotion) (time.Time, error) {
	var promotedAt time.Time
	err := tx.QueryRow(ctx, `
		WITH clock AS MATERIALIZED (
			SELECT clock_timestamp() AS now
		)
		UPDATE payload_stage_write_attempts AS attempt
		SET
			state = 'promoted',
			lease_owner = NULL,
			lease_token = NULL,
			lease_expires_at = NULL,
			provider_staging_version = $7,
			provider_final_version = $8,
			promotion_mode = $9,
			expected_final_version = $10,
			promoted_at = clock.now
		FROM clock, payload_records AS payload
		WHERE attempt.assignment_id = $1
		  AND attempt.payload_id = $2
		  AND attempt.stage_write_attempt_id = $3
		  AND attempt.attempt_number = $4
		  AND attempt.state = 'leased'
		  AND attempt.lease_owner = $5
		  AND attempt.lease_token = $6
		  AND attempt.lease_expires_at > clock.now
		  AND attempt.staging_object_key = $11
		  AND attempt.final_object_key = $12
		  AND payload.assignment_id = attempt.assignment_id
		  AND payload.payload_id = attempt.payload_id
		  AND payload.ledger_id = $13
		  AND payload.state = 'staging'
		  AND payload.staging_object_key = attempt.staging_object_key
		  AND payload.final_object_key = attempt.final_object_key
		  AND payload.expires_at > clock.now
		RETURNING attempt.promoted_at
	`,
		lease.AssignmentID,
		lease.PayloadID,
		lease.StageAttemptID,
		lease.Attempt,
		lease.Owner,
		lease.Token,
		promotion.ProviderStagingVersion,
		promotion.ProviderFinalVersion,
		promotion.Mode,
		nullIfEmpty(promotion.ExpectedFinalVersion),
		lease.StagingObjectKey,
		lease.FinalObjectKey,
		lease.LedgerID,
	).Scan(&promotedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return time.Time{}, ErrLeaseLost
	}
	if err != nil {
		return time.Time{}, fmt.Errorf("fence object provider promotion: %w", err)
	}
	return promotedAt, nil
}

// finalizePayloadRecord marks the staging payload row finalized under the fenced
// object keys, returning ErrLeaseLost if it was concurrently changed.
func finalizePayloadRecord(ctx context.Context, tx pgx.Tx, lease ObjectStageWriteLease, promotedAt time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE payload_records
		SET state = 'finalized', finalized_at = $6
		WHERE assignment_id = $1
		  AND ledger_id = $2
		  AND payload_id = $3
		  AND state = 'staging'
		  AND staging_object_key = $4
		  AND final_object_key = $5
	`,
		lease.AssignmentID,
		lease.LedgerID,
		lease.PayloadID,
		lease.StagingObjectKey,
		lease.FinalObjectKey,
		promotedAt,
	)
	if err != nil {
		return fmt.Errorf("finalize object payload metadata: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrLeaseLost
	}
	return nil
}
