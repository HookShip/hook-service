package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// StageObjectPayload reserves exact object and cryptographic metadata before
// provider I/O. Retries must first load any existing stage. It records the
// staging intent, the initial ready attempt, and verifies the durable identity
// matches exactly, all under one transaction.
func (r *objectStageStore) StageObjectPayload(ctx context.Context, stage ObjectPayloadStage) error {
	stagingKey, finalKey, err := prepareStageIntent(&stage)
	if err != nil {
		return err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object payload staging: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	if err := lockPendingLedger(ctx, tx, stage.AssignmentID, stage.LedgerID); err != nil {
		return err
	}
	if err := insertObjectPayloadRow(ctx, tx, stage, stagingKey, finalKey); err != nil {
		return err
	}
	if err := insertInitialStageAttempt(ctx, tx, stage); err != nil {
		return err
	}
	stored, err := readObjectStageForUpdate(ctx, tx, stage.AssignmentID, stage.LedgerID)
	if err != nil {
		return err
	}
	if err := verifyStagedIdentity(stored, stage, stagingKey, finalKey); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit object payload staging: %w", err)
	}
	return nil
}

// prepareStageIntent validates a staging request, normalizes its timestamps to
// UTC microsecond resolution, and derives the deterministic object keys.
func prepareStageIntent(stage *ObjectPayloadStage) (stagingKey, finalKey string, err error) {
	for name, value := range map[string]string{
		"assignment ID": stage.AssignmentID,
		"ledger ID":     stage.LedgerID,
		"payload ID":    stage.PayloadID,
	} {
		if err := validateText(name, value, 255); err != nil {
			return "", "", err
		}
	}
	if stage.StagedAt.IsZero() {
		stage.StagedAt = time.Now().UTC()
	}
	stage.StagedAt = stage.StagedAt.UTC().Truncate(time.Microsecond)
	stage.ExpiresAt = stage.ExpiresAt.UTC().Truncate(time.Microsecond)
	if err := validatePayloadCrypto(
		stage.KeyReference,
		stage.KeyVersion,
		stage.Nonce,
		stage.CiphertextSize,
		stage.ExpiresAt,
		stage.StagedAt,
	); err != nil {
		return "", "", err
	}
	if stage.ExpiresAt.Before(stage.StagedAt.Add(MinimumRetryHorizon)) {
		return "", "", errors.New("object staging expiry must be at least 24 hours after staging")
	}
	return ObjectKeys(stage.AssignmentID, stage.PayloadID)
}

// lockPendingLedger row-locks the idempotency ledger and requires it to be
// pending before a payload may be staged against it.
func lockPendingLedger(ctx context.Context, tx pgx.Tx, assignmentID, ledgerID string) error {
	var ledgerState string
	err := tx.QueryRow(ctx, `
		SELECT state::text
		FROM ingest_idempotency_ledgers
		WHERE assignment_id = $1 AND ledger_id = $2
		FOR UPDATE
	`, assignmentID, ledgerID).Scan(&ledgerState)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock object payload ledger: %w", err)
	}
	if ledgerState != "pending" {
		return ErrNotPending
	}
	return nil
}

// insertObjectPayloadRow inserts the object-store payload staging row; it is a
// no-op on a retry that re-stages the same ledger.
func insertObjectPayloadRow(ctx context.Context, tx pgx.Tx, stage ObjectPayloadStage, stagingKey, finalKey string) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO payload_records (
			payload_id,
			assignment_id,
			ledger_id,
			backend,
			state,
			key_reference,
			key_version,
			nonce,
			ciphertext_sha256,
			ciphertext_size,
			staging_object_key,
			final_object_key,
			expires_at,
			created_at,
			staged_at
		) VALUES ($1, $2, $3, 'object_store', 'staging', $4, $5, $6, $7, $8, $9, $10, $11, $12, $12)
		ON CONFLICT (assignment_id, ledger_id) DO NOTHING
	`,
		stage.PayloadID,
		stage.AssignmentID,
		stage.LedgerID,
		stage.KeyReference,
		stage.KeyVersion,
		stage.Nonce,
		stage.CiphertextSHA256[:],
		stage.CiphertextSize,
		stagingKey,
		finalKey,
		stage.ExpiresAt,
		stage.StagedAt,
	)
	if err != nil {
		return fmt.Errorf("insert object payload stage: %w", err)
	}
	return nil
}

// insertInitialStageAttempt inserts the first (ready) stage-write attempt for a
// freshly staged payload; it is a no-op on retry.
func insertInitialStageAttempt(ctx context.Context, tx pgx.Tx, stage ObjectPayloadStage) error {
	initialAttemptID := stageWriteAttemptID(stage.AssignmentID, stage.PayloadID, 1)
	if _, err := tx.Exec(ctx, `
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
			created_at
		)
		SELECT
			$4,
			payload.assignment_id,
			payload.payload_id,
			payload.ledger_id,
			1,
			'ready',
			payload.staging_object_key,
			payload.final_object_key,
			payload.ciphertext_sha256,
			payload.ciphertext_size,
			payload.staged_at
		FROM payload_records AS payload
		WHERE payload.assignment_id = $1
		  AND payload.ledger_id = $2
		  AND payload.payload_id = $3
		  AND payload.backend = 'object_store'
		ON CONFLICT (assignment_id, payload_id, attempt_number) DO NOTHING
	`, stage.AssignmentID, stage.LedgerID, stage.PayloadID, initialAttemptID); err != nil {
		return fmt.Errorf("insert initial object stage write attempt: %w", err)
	}
	return nil
}

// readObjectStageForUpdate reads back and row-locks the durable staged identity.
func readObjectStageForUpdate(ctx context.Context, tx pgx.Tx, assignmentID, ledgerID string) (ObjectPayloadStageRecord, error) {
	stored, err := scanObjectPayloadStage(tx.QueryRow(ctx, objectStageSelect+`
		WHERE payload.assignment_id = $1 AND payload.ledger_id = $2
		FOR UPDATE OF payload
	`, assignmentID, ledgerID))
	if err != nil {
		return ObjectPayloadStageRecord{}, fmt.Errorf("read object payload stage: %w", err)
	}
	return stored, nil
}

// verifyStagedIdentity fails closed if the durably stored identity does not
// exactly match the requested staging identity, or if it was already reclaimed.
func verifyStagedIdentity(stored ObjectPayloadStageRecord, stage ObjectPayloadStage, stagingKey, finalKey string) error {
	if stored.PayloadID != stage.PayloadID ||
		stored.KeyReference != stage.KeyReference ||
		stored.KeyVersion != stage.KeyVersion ||
		!bytes.Equal(stored.Nonce, stage.Nonce) ||
		stored.CiphertextSHA256 != stage.CiphertextSHA256 ||
		stored.CiphertextSize != stage.CiphertextSize ||
		stored.StagingObjectKey != stagingKey ||
		stored.FinalObjectKey != finalKey ||
		!stored.ExpiresAt.Equal(stage.ExpiresAt) ||
		!stored.StagedAt.Equal(stage.StagedAt) {
		return ErrPayloadConflict
	}
	if stored.State == "orphaned" || stored.State == "deleting" || stored.State == "deleted" {
		return ErrPayloadUnavailable
	}
	return nil
}

// GetObjectPayloadStage returns the exact durable object identity and
// cryptographic metadata that a restarted caller must resume.
func (r *objectStageStore) GetObjectPayloadStage(
	ctx context.Context,
	assignmentID, ledgerID string,
) (ObjectPayloadStageRecord, error) {
	stage, err := scanObjectPayloadStage(r.pool.QueryRow(ctx, objectStageSelect+`
		WHERE payload.assignment_id = $1
		  AND payload.ledger_id = $2
		  AND payload.backend = 'object_store'
	`, assignmentID, ledgerID))
	if errors.Is(err, pgx.ErrNoRows) {
		return ObjectPayloadStageRecord{}, ErrNotFound
	}
	if err != nil {
		return ObjectPayloadStageRecord{}, fmt.Errorf("get object payload stage: %w", err)
	}
	return stage, nil
}
