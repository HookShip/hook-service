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

func (r *idempotencyStore) ReserveIdempotency(
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
	if err := validateStableHeaders(request.Headers); err != nil {
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

func (r *idempotencyStore) GetReservation(
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
