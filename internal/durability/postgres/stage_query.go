package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// objectStageSelect is the shared projection and join for reading an
// object-store payload together with its current stage-write attempt. Callers
// append their own WHERE (and optional FOR UPDATE) clause. It is a compile-time
// constant, so no runtime value is ever interpolated into SQL.
const objectStageSelect = `
	SELECT
		payload.assignment_id,
		payload.ledger_id,
		payload.payload_id,
		payload.state::text,
		payload.key_reference,
		payload.key_version,
		payload.nonce,
		payload.ciphertext_sha256,
		payload.ciphertext_size,
		attempt.stage_write_attempt_id,
		attempt.attempt_number,
		payload.staging_object_key,
		payload.final_object_key,
		payload.expires_at,
		payload.staged_at,
		payload.finalized_at
	FROM payload_records AS payload
	JOIN payload_stage_write_attempts AS attempt
	  ON attempt.assignment_id = payload.assignment_id
	 AND attempt.payload_id = payload.payload_id
	 AND attempt.staging_object_key = payload.staging_object_key`

// ListObjectStageCleanup returns every durable attempt identity, including
// stale attempts, even after payload deletion completes.
func (r *objectStageStore) ListObjectStageCleanup(
	ctx context.Context,
	assignmentID, payloadID string,
) ([]ObjectStageCleanupRecord, error) {
	rows, err := r.pool.Query(ctx, `
		SELECT
			assignment_id,
			payload_id,
			stage_write_attempt_id,
			attempt_number,
			state::text,
			staging_object_key,
			final_object_key,
			COALESCE(provider_staging_version, ''),
			COALESCE(provider_final_version, '')
		FROM payload_stage_write_attempts
		WHERE assignment_id = $1 AND payload_id = $2
		ORDER BY attempt_number
	`, assignmentID, payloadID)
	if err != nil {
		return nil, fmt.Errorf("list object stage cleanup identities: %w", err)
	}
	defer rows.Close()

	var result []ObjectStageCleanupRecord
	for rows.Next() {
		var item ObjectStageCleanupRecord
		if err := rows.Scan(
			&item.AssignmentID,
			&item.PayloadID,
			&item.StageAttemptID,
			&item.Attempt,
			&item.State,
			&item.StagingObjectKey,
			&item.FinalObjectKey,
			&item.ProviderStagingVersion,
			&item.ProviderFinalVersion,
		); err != nil {
			return nil, fmt.Errorf("scan object stage cleanup identity: %w", err)
		}
		result = append(result, item)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate object stage cleanup identities: %w", err)
	}
	return result, nil
}

func scanObjectPayloadStage(row pgx.Row) (ObjectPayloadStageRecord, error) {
	var result ObjectPayloadStageRecord
	var checksum []byte
	var finalizedAt pgtype.Timestamptz
	err := row.Scan(
		&result.AssignmentID,
		&result.LedgerID,
		&result.PayloadID,
		&result.State,
		&result.KeyReference,
		&result.KeyVersion,
		&result.Nonce,
		&checksum,
		&result.CiphertextSize,
		&result.StageAttemptID,
		&result.StageAttempt,
		&result.StagingObjectKey,
		&result.FinalObjectKey,
		&result.ExpiresAt,
		&result.StagedAt,
		&finalizedAt,
	)
	if err != nil {
		return ObjectPayloadStageRecord{}, err
	}
	if len(checksum) != len(result.CiphertextSHA256) {
		return ObjectPayloadStageRecord{}, errors.New("stored object payload checksum has invalid length")
	}
	copy(result.CiphertextSHA256[:], checksum)
	if finalizedAt.Valid {
		value := finalizedAt.Time
		result.FinalizedAt = &value
	}
	return result, nil
}

func validatePayloadCrypto(
	keyReference string,
	keyVersion string,
	nonce []byte,
	size int64,
	expiresAt, createdAt time.Time,
) error {
	if err := validateText("key reference", keyReference, 512); err != nil {
		return err
	}
	if err := validateText("key version", keyVersion, 128); err != nil {
		return err
	}
	if len(nonce) < 12 || len(nonce) > 32 {
		return errors.New("ciphertext nonce must be between 12 and 32 bytes")
	}
	if size <= 0 {
		return errors.New("ciphertext size must be positive")
	}
	if createdAt.IsZero() || expiresAt.IsZero() || !expiresAt.After(createdAt) {
		return errors.New("payload expiry must be after creation")
	}
	return nil
}
