package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"github.com/jackc/pgx/v5"
	"time"
)

func (r *acceptanceStore) PutEndpointSnapshot(
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

func (r *acceptanceStore) AcceptPostgres(
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

func (r *acceptanceStore) IngestExists(ctx context.Context, assignmentID, ingestID string) (bool, error) {
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
