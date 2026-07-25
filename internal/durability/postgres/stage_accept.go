package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// AcceptFinalizedObject crosses the protocol-v1 acceptance barrier for a
// finalized object-store payload: immutable ingest, ledger commit, payload
// commit, and ready dispatch job all become durable atomically or not at all.
func (r *objectStageStore) AcceptFinalizedObject(
	ctx context.Context,
	acceptance ObjectAcceptance,
) error {
	acceptedAt, expiresAt, tombstoneExpiresAt, err := validateAcceptance(&acceptance)
	if err != nil {
		return err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reservation, err := lockPendingReservation(ctx, tx, acceptance.AssignmentID, acceptance.IdempotencyKey, acceptance.LedgerID)
	if err != nil {
		return err
	}
	if err := lockFinalizedPayload(ctx, tx, acceptance, acceptedAt); err != nil {
		return err
	}
	if err := insertIngest(ctx, tx, reservation, acceptance.IngestID, acceptedAt, expiresAt); err != nil {
		return err
	}
	if err := commitObjectPayload(ctx, tx, acceptance, acceptedAt); err != nil {
		return err
	}
	if err := insertDispatch(
		ctx,
		tx,
		reservation,
		acceptance.IngestID,
		acceptance.PayloadID,
		acceptance.DeliveryID,
		acceptance.DispatchJobID,
		acceptance.EndpointSnapshotID,
		acceptance.DispatchRunAt.UTC(),
		acceptance.DispatchMaxAttempts,
		acceptedAt,
	); err != nil {
		return err
	}
	if err := commitReservation(
		ctx,
		tx,
		reservation,
		acceptance.IngestID,
		acceptance.PayloadID,
		acceptance.DispatchJobID,
		acceptedAt,
		expiresAt,
		tombstoneExpiresAt,
	); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit object acceptance barrier: %w", err)
	}
	return nil
}

// validateAcceptance normalizes the acceptance timestamps and validates its
// dispatch bounds, defaulting the dispatch run time to acceptance.
func validateAcceptance(acceptance *ObjectAcceptance) (acceptedAt, expiresAt, tombstoneExpiresAt time.Time, err error) {
	acceptedAt, expiresAt, tombstoneExpiresAt, err = normalizeTimes(acceptance.AcceptedAt, acceptance.IdempotencyExpires)
	if err != nil {
		return time.Time{}, time.Time{}, time.Time{}, err
	}
	if acceptance.DispatchRunAt.IsZero() {
		acceptance.DispatchRunAt = acceptedAt
	}
	if acceptance.DispatchMaxAttempts < 1 || acceptance.DispatchMaxAttempts > 1000 {
		return time.Time{}, time.Time{}, time.Time{}, errors.New("dispatch max attempts must be between 1 and 1000")
	}
	return acceptedAt, expiresAt, tombstoneExpiresAt, nil
}

// lockFinalizedPayload row-locks the payload and requires it to be a finalized,
// still-unexpired object payload before acceptance.
func lockFinalizedPayload(ctx context.Context, tx pgx.Tx, acceptance ObjectAcceptance, acceptedAt time.Time) error {
	var payloadState string
	var payloadExpiresAt time.Time
	err := tx.QueryRow(ctx, `
		SELECT state::text, expires_at
		FROM payload_records
		WHERE assignment_id = $1
		  AND ledger_id = $2
		  AND payload_id = $3
		  AND backend = 'object_store'
		FOR UPDATE
	`, acceptance.AssignmentID, acceptance.LedgerID, acceptance.PayloadID).Scan(
		&payloadState,
		&payloadExpiresAt,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock finalized object payload: %w", err)
	}
	if payloadState != "finalized" {
		return fmt.Errorf("object payload is %s, not finalized", payloadState)
	}
	var payloadUnexpired bool
	if err := tx.QueryRow(ctx, `
		SELECT expires_at > clock_timestamp()
		FROM payload_records
		WHERE assignment_id = $1 AND payload_id = $2
	`, acceptance.AssignmentID, acceptance.PayloadID).Scan(&payloadUnexpired); err != nil {
		return fmt.Errorf("check finalized object payload expiry: %w", err)
	}
	if !payloadUnexpired || !payloadExpiresAt.After(acceptedAt) {
		return errors.New("finalized object payload has expired")
	}
	return nil
}

// commitObjectPayload binds the payload to its ingest and marks it committed,
// returning ErrPayloadConflict if it was not still finalized.
func commitObjectPayload(ctx context.Context, tx pgx.Tx, acceptance ObjectAcceptance, acceptedAt time.Time) error {
	tag, err := tx.Exec(ctx, `
		UPDATE payload_records
		SET ingest_id = $3, state = 'committed', committed_at = $4
		WHERE assignment_id = $1 AND payload_id = $2 AND state = 'finalized'
	`, acceptance.AssignmentID, acceptance.PayloadID, acceptance.IngestID, acceptedAt)
	if err != nil {
		return fmt.Errorf("commit object payload metadata: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrPayloadConflict
	}
	return nil
}
