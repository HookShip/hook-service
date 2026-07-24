package postgres

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"time"
)

// StageObjectPayload reserves exact object and cryptographic metadata before
// provider I/O. Retries must first load any existing stage.
func (r *objectStageStore) StageObjectPayload(
	ctx context.Context,
	stage ObjectPayloadStage,
) error {
	for name, value := range map[string]string{
		"assignment ID": stage.AssignmentID,
		"ledger ID":     stage.LedgerID,
		"payload ID":    stage.PayloadID,
	} {
		if err := validateText(name, value, 255); err != nil {
			return err
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
		return err
	}
	if stage.ExpiresAt.Before(stage.StagedAt.Add(MinimumRetryHorizon)) {
		return errors.New("object staging expiry must be at least 24 hours after staging")
	}
	stagingKey, finalKey, err := ObjectKeys(stage.AssignmentID, stage.PayloadID)
	if err != nil {
		return err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object payload staging: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var ledgerState string
	err = tx.QueryRow(ctx, `
		SELECT state::text
		FROM ingest_idempotency_ledgers
		WHERE assignment_id = $1 AND ledger_id = $2
		FOR UPDATE
	`, stage.AssignmentID, stage.LedgerID).Scan(&ledgerState)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return fmt.Errorf("lock object payload ledger: %w", err)
	}
	if ledgerState != "pending" {
		return ErrNotPending
	}

	_, err = tx.Exec(ctx, `
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

	stored, err := scanObjectPayloadStage(tx.QueryRow(ctx, `
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
		 AND attempt.staging_object_key = payload.staging_object_key
		WHERE payload.assignment_id = $1 AND payload.ledger_id = $2
		FOR UPDATE OF payload
	`, stage.AssignmentID, stage.LedgerID))
	if err != nil {
		return fmt.Errorf("read object payload stage: %w", err)
	}
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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit object payload staging: %w", err)
	}
	return nil
}

// GetObjectPayloadStage returns the exact durable object identity and
// cryptographic metadata that a restarted caller must resume.
func (r *objectStageStore) GetObjectPayloadStage(
	ctx context.Context,
	assignmentID, ledgerID string,
) (ObjectPayloadStageRecord, error) {
	stage, err := scanObjectPayloadStage(r.pool.QueryRow(ctx, `
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
		 AND attempt.staging_object_key = payload.staging_object_key
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

// ClaimObjectStageWrite claims the current attempt or replaces an expired
// attempt with a new deterministic staging identity.
func (r *objectStageStore) ClaimObjectStageWrite(
	ctx context.Context,
	assignmentID, ledgerID, owner, token string,
	leaseDuration time.Duration,
) (ObjectStageWriteLease, bool, error) {
	for name, value := range map[string]string{
		"assignment ID": assignmentID,
		"ledger ID":     ledgerID,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return ObjectStageWriteLease{}, false, err
		}
	}
	leaseMicroseconds, err := boundedDurationMicroseconds(
		"object stage write lease duration",
		leaseDuration,
		time.Hour,
		false,
	)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("begin object stage write claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var lease ObjectStageWriteLease
	var payloadState, attemptState string
	var nonce, checksum []byte
	err = tx.QueryRow(ctx, `
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
			payload.staging_object_key,
			payload.final_object_key,
			attempt.stage_write_attempt_id,
			attempt.attempt_number,
			attempt.state::text
		FROM payload_records AS payload
		JOIN payload_stage_write_attempts AS attempt
		  ON attempt.assignment_id = payload.assignment_id
		 AND attempt.payload_id = payload.payload_id
		 AND attempt.staging_object_key = payload.staging_object_key
		WHERE payload.assignment_id = $1
		  AND payload.ledger_id = $2
		  AND payload.backend = 'object_store'
		FOR UPDATE OF payload, attempt
	`, assignmentID, ledgerID).Scan(
		&lease.AssignmentID,
		&lease.LedgerID,
		&lease.PayloadID,
		&payloadState,
		&lease.KeyReference,
		&lease.KeyVersion,
		&nonce,
		&checksum,
		&lease.CiphertextSize,
		&lease.StagingObjectKey,
		&lease.FinalObjectKey,
		&lease.StageAttemptID,
		&lease.Attempt,
		&attemptState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ObjectStageWriteLease{}, false, ErrNotFound
	}
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("lock object stage write attempt: %w", err)
	}
	var payloadUnexpired, leaseActive bool
	if err := tx.QueryRow(ctx, `
		SELECT
			payload.expires_at > clock_timestamp(),
			COALESCE(attempt.lease_expires_at > clock_timestamp(), false)
		FROM payload_records AS payload
		JOIN payload_stage_write_attempts AS attempt
		  ON attempt.assignment_id = payload.assignment_id
		 AND attempt.payload_id = payload.payload_id
		 AND attempt.stage_write_attempt_id = $3
		WHERE payload.assignment_id = $1
		  AND payload.ledger_id = $2
	`, assignmentID, ledgerID, lease.StageAttemptID).Scan(
		&payloadUnexpired,
		&leaseActive,
	); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("check object stage lease clock: %w", err)
	}
	if payloadState != "staging" || !payloadUnexpired {
		return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
	}
	if len(checksum) != len(lease.CiphertextSHA256) {
		return ObjectStageWriteLease{}, false, errors.New("stored stage write checksum has invalid length")
	}
	lease.Nonce = append([]byte(nil), nonce...)
	copy(lease.CiphertextSHA256[:], checksum)
	lease.Owner = owner
	lease.Token = token

	if attemptState == "leased" && leaseActive {
		if err := tx.Commit(ctx); err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("commit active object stage claim: %w", err)
		}
		return ObjectStageWriteLease{}, false, nil
	}

	if attemptState == "ready" {
		lease.ExpiresAt, err = r.claimReadyAttempt(ctx, tx, assignmentID, lease.StageAttemptID, owner, token, leaseMicroseconds)
		if err != nil {
			return ObjectStageWriteLease{}, false, err
		}
	} else {
		// The prior attempt's lease expired: reclaim it and lease a fresh
		// replacement attempt with new deterministic object keys.
		if err := r.orphanExpiredLeasedAttempt(ctx, tx, assignmentID, lease.StageAttemptID); err != nil {
			return ObjectStageWriteLease{}, false, err
		}
		nextAttempt := lease.Attempt + 1
		nextAttemptID, nextStagingKey, nextFinalKey, err := stageAttemptKeys(assignmentID, lease.PayloadID, nextAttempt)
		if err != nil {
			return ObjectStageWriteLease{}, false, err
		}
		lease.ExpiresAt, err = r.insertLeasedAttempt(ctx, tx, leasedAttempt{
			assignmentID: assignmentID,
			ledgerID:     ledgerID,
			attemptID:    nextAttemptID,
			attempt:      nextAttempt,
			stagingKey:   nextStagingKey,
			finalKey:     nextFinalKey,
			checksum:     lease.CiphertextSHA256[:],
			size:         lease.CiphertextSize,
			owner:        owner,
			token:        token,
			leaseMicros:  leaseMicroseconds,
		})
		if err != nil {
			return ObjectStageWriteLease{}, false, err
		}
		if err := r.moveStagedIdentityKeys(ctx, tx, assignmentID, ledgerID, nextStagingKey, nextFinalKey); err != nil {
			return ObjectStageWriteLease{}, false, err
		}
		lease.StageAttemptID = nextAttemptID
		lease.Attempt = nextAttempt
		lease.StagingObjectKey = nextStagingKey
		lease.FinalObjectKey = nextFinalKey
	}

	if err := tx.Commit(ctx); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("commit object stage write claim: %w", err)
	}
	return lease, true, nil
}

// SupersedeObjectStage atomically replaces the staged ciphertext identity of a
// still-`staging` object payload with a new identity produced by a retry (a
// fresh random-nonce re-seal), fences every prior stage-write attempt, and
// creates a new leased attempt with fresh deterministic object keys. Superseded
// attempts are retained (state 'orphaned') so their objects are reclaimed by
// orphan cleanup, and prior objects can never be accepted under mismatched
// metadata.
//
// It guarantees a single winner under concurrent retries: the payload row is
// locked FOR UPDATE, and if any stage-write attempt still holds an active
// (unexpired) lease the call returns ok=false without changing anything, so a
// concurrent retry backs off. It never supersedes a finalized or committed
// payload (returns ok=false so the caller resumes the durable object instead).
// The returned lease belongs to the new attempt and fences finalization; any
// stale writer holding a prior attempt's lease can no longer finalize.
func (r *objectStageStore) SupersedeObjectStage(
	ctx context.Context,
	identity ObjectPayloadStage,
	owner, token string,
	leaseDuration time.Duration,
) (ObjectStageWriteLease, bool, error) {
	for name, value := range map[string]string{
		"assignment ID": identity.AssignmentID,
		"ledger ID":     identity.LedgerID,
		"payload ID":    identity.PayloadID,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return ObjectStageWriteLease{}, false, err
		}
	}
	if err := validateText("key reference", identity.KeyReference, 512); err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	if err := validateText("key version", identity.KeyVersion, 128); err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	if len(identity.Nonce) < 12 || len(identity.Nonce) > 32 {
		return ObjectStageWriteLease{}, false, errors.New("ciphertext nonce must be between 12 and 32 bytes")
	}
	if identity.CiphertextSize <= 0 {
		return ObjectStageWriteLease{}, false, errors.New("ciphertext size must be positive")
	}
	leaseMicroseconds, err := boundedDurationMicroseconds(
		"object stage write lease duration",
		leaseDuration,
		time.Hour,
		false,
	)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("begin object stage supersession: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var state, keyReference, keyVersion string
	var payloadUnexpired bool
	err = tx.QueryRow(ctx, `
		SELECT state::text, key_reference, key_version, expires_at > clock_timestamp()
		FROM payload_records
		WHERE assignment_id = $1 AND ledger_id = $2 AND payload_id = $3 AND backend = 'object_store'
		FOR UPDATE
	`, identity.AssignmentID, identity.LedgerID, identity.PayloadID).Scan(
		&state, &keyReference, &keyVersion, &payloadUnexpired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return ObjectStageWriteLease{}, false, ErrNotFound
	}
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("lock object payload for supersession: %w", err)
	}
	// Never rebind a finalized or committed payload; the caller resumes the
	// durable object. Reclaimed/deleting payloads are also not supersedable.
	if state != "staging" {
		return ObjectStageWriteLease{}, false, nil
	}
	if !payloadUnexpired {
		return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
	}
	// The data encryption key must be stable across the retry; a different key
	// reference/version is a genuine conflict, not a supersession.
	if keyReference != identity.KeyReference || keyVersion != identity.KeyVersion {
		return ObjectStageWriteLease{}, false, ErrPayloadConflict
	}

	// Single-winner fence: if any attempt still holds an active lease, a
	// concurrent writer owns this payload; the caller must back off.
	activeWriter, err := r.hasActiveWriter(ctx, tx, identity.AssignmentID, identity.PayloadID)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	if activeWriter {
		return ObjectStageWriteLease{}, false, nil
	}

	// Fence every prior attempt so no stale writer can finalize, then allocate
	// and lease a fresh attempt with new deterministic object keys.
	if err := r.fencePriorAttempts(ctx, tx, identity.AssignmentID, identity.PayloadID); err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	maxAttempt, err := r.maxAttemptNumber(ctx, tx, identity.AssignmentID, identity.PayloadID)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	nextAttempt := maxAttempt + 1
	nextAttemptID, nextStagingKey, nextFinalKey, err := stageAttemptKeys(identity.AssignmentID, identity.PayloadID, nextAttempt)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	leaseExpiresAt, err := r.insertLeasedAttempt(ctx, tx, leasedAttempt{
		assignmentID: identity.AssignmentID,
		ledgerID:     identity.LedgerID,
		attemptID:    nextAttemptID,
		attempt:      nextAttempt,
		stagingKey:   nextStagingKey,
		finalKey:     nextFinalKey,
		checksum:     identity.CiphertextSHA256[:],
		size:         identity.CiphertextSize,
		owner:        owner,
		token:        token,
		leaseMicros:  leaseMicroseconds,
	})
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	// Move the payload's staged identity to the new attempt. The relaxed payload
	// guard permits this fenced ciphertext-identity change only because the new
	// leased attempt above records the matching keys, checksum, and size.
	tag, err := tx.Exec(ctx, `
		UPDATE payload_records
		SET nonce = $4,
			ciphertext_sha256 = $5,
			ciphertext_size = $6,
			staging_object_key = $7,
			final_object_key = $8
		WHERE assignment_id = $1 AND ledger_id = $2 AND payload_id = $3 AND state = 'staging'
	`,
		identity.AssignmentID,
		identity.LedgerID,
		identity.PayloadID,
		identity.Nonce,
		identity.CiphertextSHA256[:],
		identity.CiphertextSize,
		nextStagingKey,
		nextFinalKey,
	)
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("supersede object payload identity: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
	}
	if err := tx.Commit(ctx); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("commit object stage supersession: %w", err)
	}

	lease := ObjectStageWriteLease{
		AssignmentID:     identity.AssignmentID,
		LedgerID:         identity.LedgerID,
		PayloadID:        identity.PayloadID,
		StageAttemptID:   nextAttemptID,
		Attempt:          nextAttempt,
		StagingObjectKey: nextStagingKey,
		FinalObjectKey:   nextFinalKey,
		KeyReference:     identity.KeyReference,
		KeyVersion:       identity.KeyVersion,
		Nonce:            append([]byte(nil), identity.Nonce...),
		CiphertextSHA256: identity.CiphertextSHA256,
		CiphertextSize:   identity.CiphertextSize,
		Owner:            owner,
		Token:            token,
		ExpiresAt:        leaseExpiresAt,
	}
	return lease, true, nil
}

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

// FinalizeObjectPayload records a conditionally promoted provider object only
// while the stage-write lease is still current.
func (r *objectStageStore) FinalizeObjectPayload(
	ctx context.Context,
	lease ObjectStageWriteLease,
	promotion ObjectPromotion,
) error {
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

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object payload finalization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var promotedAt time.Time
	err = tx.QueryRow(ctx, `
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
		return ErrLeaseLost
	}
	if err != nil {
		return fmt.Errorf("fence object provider promotion: %w", err)
	}
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
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit object payload finalization: %w", err)
	}
	return nil
}

func (r *objectStageStore) AcceptFinalizedObject(
	ctx context.Context,
	acceptance ObjectAcceptance,
) error {
	acceptedAt, expiresAt, tombstoneExpiresAt, err := normalizeTimes(
		acceptance.AcceptedAt,
		acceptance.IdempotencyExpires,
	)
	if err != nil {
		return err
	}
	if acceptance.DispatchRunAt.IsZero() {
		acceptance.DispatchRunAt = acceptedAt
	}
	if acceptance.DispatchMaxAttempts < 1 || acceptance.DispatchMaxAttempts > 1000 {
		return errors.New("dispatch max attempts must be between 1 and 1000")
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return fmt.Errorf("begin object acceptance: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	reservation, err := lockPendingReservation(
		ctx,
		tx,
		acceptance.AssignmentID,
		acceptance.IdempotencyKey,
		acceptance.LedgerID,
	)
	if err != nil {
		return err
	}
	var payloadState string
	var payloadExpiresAt time.Time
	err = tx.QueryRow(ctx, `
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
	if err := insertIngest(
		ctx,
		tx,
		reservation,
		acceptance.IngestID,
		acceptedAt,
		expiresAt,
	); err != nil {
		return err
	}
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
