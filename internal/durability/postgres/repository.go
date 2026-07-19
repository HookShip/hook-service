package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
)

type Repository struct {
	pool *pgxpool.Pool
}

func New(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	return &Repository{pool: pool}, nil
}

func (r *Repository) ReserveIdempotency(
	ctx context.Context,
	request ReservationRequest,
) (ReservationResult, error) {
	if err := validateText("ledger ID", request.LedgerID, 255); err != nil {
		return ReservationResult{}, err
	}
	if err := validateText("assignment ID", request.AssignmentID, 255); err != nil {
		return ReservationResult{}, err
	}
	if err := validateText("idempotency key", request.IdempotencyKey, 255); err != nil {
		return ReservationResult{}, err
	}
	if err := request.Headers.validate(); err != nil {
		return ReservationResult{}, err
	}
	if request.ReservedAt.IsZero() {
		request.ReservedAt = time.Now().UTC()
	}
	request.ReservedAt = request.ReservedAt.UTC().Truncate(time.Microsecond)
	fingerprint := RequestFingerprint(request.BodySHA256, request.Headers)

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ReservationResult{}, fmt.Errorf("begin idempotency reservation: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	tag, err := tx.Exec(ctx, `
		INSERT INTO ingest_idempotency_ledgers (
			ledger_id,
			assignment_id,
			idempotency_key,
			request_fingerprint,
			body_sha256,
			event_id,
			event_type,
			route,
			content_type,
			state,
			reserved_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, 'pending', $10, $10)
		ON CONFLICT (assignment_id, idempotency_key) DO NOTHING
	`,
		request.LedgerID,
		request.AssignmentID,
		request.IdempotencyKey,
		fingerprint[:],
		request.BodySHA256[:],
		request.Headers.EventID,
		request.Headers.EventType,
		request.Headers.Route,
		request.Headers.ContentType,
		request.ReservedAt,
	)
	if err != nil {
		return ReservationResult{}, fmt.Errorf("insert idempotency reservation: %w", err)
	}

	reservation, err := scanReservation(tx.QueryRow(ctx, `
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
		WHERE assignment_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, request.AssignmentID, request.IdempotencyKey))
	if err != nil {
		return ReservationResult{}, fmt.Errorf("read idempotency reservation: %w", err)
	}

	if reservation.State == "committed" {
		expired, err := tx.Exec(ctx, `
			WITH clock AS MATERIALIZED (
				SELECT clock_timestamp() AS now
			)
			UPDATE ingest_idempotency_ledgers
			SET state = 'tombstone', updated_at = clock.now
			FROM clock
			WHERE assignment_id = $1
			  AND idempotency_key = $2
			  AND state = 'committed'
			  AND idempotency_expires_at <= clock.now
		`, request.AssignmentID, request.IdempotencyKey)
		if err != nil {
			return ReservationResult{}, fmt.Errorf("expire idempotency reservation: %w", err)
		}
		if expired.RowsAffected() == 1 {
			reservation.State = "tombstone"
		}
	}

	result := ReservationResult{
		Created:     tag.RowsAffected() == 1,
		Reservation: reservation,
	}
	switch {
	case reservation.State == "tombstone":
		result.Status = ReservationExpired
	case !bytes.Equal(reservation.BodySHA256[:], request.BodySHA256[:]):
		result.Status = ReservationConflict
	case reservation.Headers != request.Headers ||
		!bytes.Equal(reservation.RequestFingerprint[:], fingerprint[:]):
		result.Status = ReservationMetadataConflict
	case reservation.State == "committed":
		result.Status = ReservationDuplicate
	default:
		result.Status = ReservationPending
	}

	if err := tx.Commit(ctx); err != nil {
		return ReservationResult{}, fmt.Errorf("commit idempotency reservation: %w", err)
	}
	return result, nil
}

func (r *Repository) GetReservation(
	ctx context.Context,
	assignmentID, idempotencyKey string,
) (Reservation, error) {
	reservation, err := scanReservation(r.pool.QueryRow(ctx, `
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
		WHERE assignment_id = $1 AND idempotency_key = $2
	`, assignmentID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ErrNotFound
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("get idempotency reservation: %w", err)
	}
	return reservation, nil
}

func (r *Repository) PutEndpointSnapshot(
	ctx context.Context,
	snapshot EndpointSnapshot,
) (EndpointSnapshot, error) {
	for name, value := range map[string]string{
		"snapshot ID":   snapshot.SnapshotID,
		"assignment ID": snapshot.AssignmentID,
		"endpoint ID":   snapshot.EndpointID,
		"bundle ID":     snapshot.BundleID,
	} {
		if err := validateText(name, value, 255); err != nil {
			return EndpointSnapshot{}, err
		}
	}
	if snapshot.ConfigGeneration < 1 || snapshot.ConfigGeneration > 9007199254740991 {
		return EndpointSnapshot{}, errors.New("configuration generation is out of range")
	}
	if !json.Valid(snapshot.Content) {
		return EndpointSnapshot{}, errors.New("endpoint snapshot content must be valid JSON")
	}
	if len(snapshot.Content) > 1<<20 {
		return EndpointSnapshot{}, errors.New("endpoint snapshot content exceeds 1 MiB")
	}
	actualChecksum := SHA256(snapshot.Content)
	if snapshot.ContentSHA256 != (Hash{}) && snapshot.ContentSHA256 != actualChecksum {
		return EndpointSnapshot{}, errors.New("endpoint snapshot checksum does not match content")
	}
	snapshot.ContentSHA256 = actualChecksum
	if snapshot.CreatedAt.IsZero() {
		snapshot.CreatedAt = time.Now().UTC()
	}
	snapshot.CreatedAt = snapshot.CreatedAt.UTC().Truncate(time.Microsecond)

	_, err := r.pool.Exec(ctx, `
		INSERT INTO endpoint_snapshots (
			endpoint_snapshot_id,
			assignment_id,
			endpoint_id,
			bundle_id,
			config_generation,
			content,
			content_sha256,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (assignment_id, endpoint_snapshot_id) DO NOTHING
	`,
		snapshot.SnapshotID,
		snapshot.AssignmentID,
		snapshot.EndpointID,
		snapshot.BundleID,
		snapshot.ConfigGeneration,
		snapshot.Content,
		snapshot.ContentSHA256[:],
		snapshot.CreatedAt,
	)
	if err != nil {
		return EndpointSnapshot{}, fmt.Errorf("insert endpoint snapshot: %w", err)
	}

	var stored EndpointSnapshot
	var checksum []byte
	err = r.pool.QueryRow(ctx, `
		SELECT
			endpoint_snapshot_id,
			assignment_id,
			endpoint_id,
			bundle_id,
			config_generation,
			content,
			content_sha256,
			created_at
		FROM endpoint_snapshots
		WHERE assignment_id = $1 AND endpoint_snapshot_id = $2
	`, snapshot.AssignmentID, snapshot.SnapshotID).Scan(
		&stored.SnapshotID,
		&stored.AssignmentID,
		&stored.EndpointID,
		&stored.BundleID,
		&stored.ConfigGeneration,
		&stored.Content,
		&checksum,
		&stored.CreatedAt,
	)
	if err != nil {
		return EndpointSnapshot{}, fmt.Errorf("read endpoint snapshot: %w", err)
	}
	copy(stored.ContentSHA256[:], checksum)
	if stored.EndpointID != snapshot.EndpointID ||
		stored.BundleID != snapshot.BundleID ||
		stored.ConfigGeneration != snapshot.ConfigGeneration ||
		!bytes.Equal(stored.Content, snapshot.Content) ||
		stored.ContentSHA256 != snapshot.ContentSHA256 {
		return EndpointSnapshot{}, ErrSnapshotConflict
	}
	return stored, nil
}

func (r *Repository) AcceptPostgres(
	ctx context.Context,
	acceptance PostgresAcceptance,
) error {
	if len(acceptance.Ciphertext) == 0 || len(acceptance.Ciphertext) > MaximumPostgresPayload {
		return fmt.Errorf("PostgreSQL ciphertext must be between 1 byte and %d bytes", MaximumPostgresPayload)
	}
	if SHA256(acceptance.Ciphertext) != acceptance.CiphertextSHA256 {
		return errors.New("ciphertext checksum does not match")
	}
	if err := validatePayloadCrypto(
		acceptance.KeyReference,
		acceptance.KeyVersion,
		acceptance.Nonce,
		int64(len(acceptance.Ciphertext)),
		acceptance.PayloadExpiresAt,
		acceptance.AcceptedAt,
	); err != nil {
		return err
	}
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
		return fmt.Errorf("begin PostgreSQL acceptance: %w", err)
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
	if _, err := tx.Exec(ctx, `
		INSERT INTO payload_records (
			payload_id,
			assignment_id,
			ledger_id,
			ingest_id,
			backend,
			state,
			ciphertext,
			key_reference,
			key_version,
			nonce,
			ciphertext_sha256,
			ciphertext_size,
			expires_at,
			created_at,
			committed_at
		) VALUES ($1, $2, $3, $4, 'postgres', 'committed', $5, $6, $7, $8, $9, $10, $11, $12, $12)
	`,
		acceptance.PayloadID,
		acceptance.AssignmentID,
		acceptance.LedgerID,
		acceptance.IngestID,
		acceptance.Ciphertext,
		acceptance.KeyReference,
		acceptance.KeyVersion,
		acceptance.Nonce,
		acceptance.CiphertextSHA256[:],
		len(acceptance.Ciphertext),
		acceptance.PayloadExpiresAt.UTC(),
		acceptedAt,
	); err != nil {
		return fmt.Errorf("insert PostgreSQL payload: %w", err)
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
		return fmt.Errorf("commit PostgreSQL acceptance barrier: %w", err)
	}
	return nil
}

// StageObjectPayload reserves exact object and cryptographic metadata before
// provider I/O. Retries must first load any existing stage.
func (r *Repository) StageObjectPayload(
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
func (r *Repository) GetObjectPayloadStage(
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
func (r *Repository) ClaimObjectStageWrite(
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
		err = tx.QueryRow(ctx, `
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
		`, assignmentID, lease.StageAttemptID, owner, token, leaseMicroseconds).Scan(&lease.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
		}
		if err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("claim initial object stage write: %w", err)
		}
	} else {
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
		`, assignmentID, lease.StageAttemptID); err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("orphan stale object stage write: %w", err)
		}

		nextAttempt := lease.Attempt + 1
		nextAttemptID := stageWriteAttemptID(assignmentID, lease.PayloadID, nextAttempt)
		nextStagingKey, finalKey, err := ObjectAttemptKeys(assignmentID, lease.PayloadID, nextAttempt)
		if err != nil {
			return ObjectStageWriteLease{}, false, err
		}
		err = tx.QueryRow(ctx, `
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
				payload.ciphertext_sha256,
				payload.ciphertext_size,
				$7,
				$8,
				clock.now + $9::bigint * interval '1 microsecond',
				clock.now
			FROM payload_records AS payload
			CROSS JOIN clock
			WHERE payload.assignment_id = $1
			  AND payload.ledger_id = $2
			  AND payload.state = 'staging'
			  AND payload.expires_at > clock.now
			RETURNING lease_expires_at
		`,
			assignmentID,
			ledgerID,
			nextAttemptID,
			nextAttempt,
			nextStagingKey,
			finalKey,
			owner,
			token,
			leaseMicroseconds,
		).Scan(&lease.ExpiresAt)
		if errors.Is(err, pgx.ErrNoRows) {
			return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
		}
		if err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("insert replacement object stage write: %w", err)
		}
		if _, err := tx.Exec(ctx, `
			UPDATE payload_records
			SET staging_object_key = $3, final_object_key = $4
			WHERE assignment_id = $1
			  AND ledger_id = $2
			  AND state = 'staging'
		`, assignmentID, ledgerID, nextStagingKey, finalKey); err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("select replacement object stage identity: %w", err)
		}
		lease.StageAttemptID = nextAttemptID
		lease.Attempt = nextAttempt
		lease.StagingObjectKey = nextStagingKey
		lease.FinalObjectKey = finalKey
	}

	if err := tx.Commit(ctx); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("commit object stage write claim: %w", err)
	}
	return lease, true, nil
}

// ListObjectStageCleanup returns every durable attempt identity, including
// stale attempts, even after payload deletion completes.
func (r *Repository) ListObjectStageCleanup(
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
func (r *Repository) FinalizeObjectPayload(
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

func (r *Repository) AcceptFinalizedObject(
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

func (r *Repository) IngestExists(ctx context.Context, assignmentID, ingestID string) (bool, error) {
	var exists bool
	err := r.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM event_ingests
			WHERE assignment_id = $1 AND ingest_id = $2
		)
	`, assignmentID, ingestID).Scan(&exists)
	if err != nil {
		return false, fmt.Errorf("check ingest ownership: %w", err)
	}
	return exists, nil
}

func lockPendingReservation(
	ctx context.Context,
	tx pgx.Tx,
	assignmentID, idempotencyKey, ledgerID string,
) (Reservation, error) {
	reservation, err := scanReservation(tx.QueryRow(ctx, `
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
		WHERE assignment_id = $1 AND idempotency_key = $2
		FOR UPDATE
	`, assignmentID, idempotencyKey))
	if errors.Is(err, pgx.ErrNoRows) {
		return Reservation{}, ErrNotFound
	}
	if err != nil {
		return Reservation{}, fmt.Errorf("lock idempotency reservation: %w", err)
	}
	if reservation.LedgerID != ledgerID {
		return Reservation{}, ErrPayloadConflict
	}
	if reservation.State != "pending" {
		return Reservation{}, ErrNotPending
	}
	return reservation, nil
}

func insertIngest(
	ctx context.Context,
	tx pgx.Tx,
	reservation Reservation,
	ingestID string,
	acceptedAt, expiresAt time.Time,
) error {
	if err := validateText("ingest ID", ingestID, 255); err != nil {
		return err
	}
	_, err := tx.Exec(ctx, `
		INSERT INTO event_ingests (
			ingest_id,
			assignment_id,
			ledger_id,
			request_fingerprint,
			body_sha256,
			event_id,
			event_type,
			route,
			content_type,
			accepted_at,
			idempotency_expires_at,
			created_at
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10, $11, $10)
	`,
		ingestID,
		reservation.AssignmentID,
		reservation.LedgerID,
		reservation.RequestFingerprint[:],
		reservation.BodySHA256[:],
		reservation.Headers.EventID,
		reservation.Headers.EventType,
		reservation.Headers.Route,
		reservation.Headers.ContentType,
		acceptedAt,
		expiresAt,
	)
	if err != nil {
		return fmt.Errorf("insert immutable ingest: %w", err)
	}
	return nil
}

func insertDispatch(
	ctx context.Context,
	tx pgx.Tx,
	reservation Reservation,
	ingestID, payloadID, deliveryID, jobID, snapshotID string,
	runAt time.Time,
	maxAttempts int,
	createdAt time.Time,
) error {
	for name, value := range map[string]string{
		"payload ID":           payloadID,
		"delivery ID":          deliveryID,
		"dispatch job ID":      jobID,
		"endpoint snapshot ID": snapshotID,
	} {
		if err := validateText(name, value, 255); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO deliveries (
			delivery_id,
			assignment_id,
			ingest_id,
			payload_id,
			endpoint_snapshot_id,
			state,
			created_at,
			updated_at
		) VALUES ($1, $2, $3, $4, $5, 'ready', $6, $6)
	`,
		deliveryID,
		reservation.AssignmentID,
		ingestID,
		payloadID,
		snapshotID,
		createdAt,
	); err != nil {
		return fmt.Errorf("insert delivery: %w", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO durable_jobs (
			job_id,
			assignment_id,
			kind,
			ingest_id,
			payload_id,
			delivery_id,
			endpoint_snapshot_id,
			state,
			run_at,
			attempts,
			max_attempts,
			created_at,
			updated_at
		) VALUES ($1, $2, 'dispatch', $3, $4, $5, $6, 'ready', $7, 0, $8, $9, $9)
	`,
		jobID,
		reservation.AssignmentID,
		ingestID,
		payloadID,
		deliveryID,
		snapshotID,
		runAt,
		maxAttempts,
		createdAt,
	); err != nil {
		return fmt.Errorf("insert ready dispatch job: %w", err)
	}
	return nil
}

func commitReservation(
	ctx context.Context,
	tx pgx.Tx,
	reservation Reservation,
	ingestID, payloadID, jobID string,
	acceptedAt, expiresAt, tombstoneExpiresAt time.Time,
) error {
	tag, err := tx.Exec(ctx, `
		UPDATE ingest_idempotency_ledgers
		SET
			state = 'committed',
			ingest_id = $4,
			payload_id = $5,
			dispatch_job_id = $6,
			accepted_at = $7,
			idempotency_expires_at = $8,
			tombstone_expires_at = $9,
			updated_at = $7
		WHERE assignment_id = $1
		  AND idempotency_key = $2
		  AND ledger_id = $3
		  AND state = 'pending'
	`,
		reservation.AssignmentID,
		reservation.IdempotencyKey,
		reservation.LedgerID,
		ingestID,
		payloadID,
		jobID,
		acceptedAt,
		expiresAt,
		tombstoneExpiresAt,
	)
	if err != nil {
		return fmt.Errorf("commit idempotency ledger: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrNotPending
	}
	return nil
}

func scanReservation(row pgx.Row) (Reservation, error) {
	var result Reservation
	var fingerprint, bodyHash []byte
	var acceptedAt, expiresAt, tombstoneExpiresAt pgtype.Timestamptz
	err := row.Scan(
		&result.LedgerID,
		&result.AssignmentID,
		&result.IdempotencyKey,
		&fingerprint,
		&bodyHash,
		&result.Headers.EventID,
		&result.Headers.EventType,
		&result.Headers.Route,
		&result.Headers.ContentType,
		&result.State,
		&result.IngestID,
		&result.PayloadID,
		&result.DispatchJobID,
		&result.ReservedAt,
		&acceptedAt,
		&expiresAt,
		&tombstoneExpiresAt,
	)
	if err != nil {
		return Reservation{}, err
	}
	if len(fingerprint) != len(result.RequestFingerprint) || len(bodyHash) != len(result.BodySHA256) {
		return Reservation{}, errors.New("stored idempotency hash has invalid length")
	}
	copy(result.RequestFingerprint[:], fingerprint)
	copy(result.BodySHA256[:], bodyHash)
	if acceptedAt.Valid {
		value := acceptedAt.Time
		result.AcceptedAt = &value
	}
	if expiresAt.Valid {
		value := expiresAt.Time
		result.IdempotencyExpiresAt = &value
	}
	if tombstoneExpiresAt.Valid {
		value := tombstoneExpiresAt.Time
		result.TombstoneExpiresAt = &value
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
