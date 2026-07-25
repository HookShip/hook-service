package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

// claimTarget is the locked payload-and-attempt row scanned before a claim
// decides whether to lease the current attempt or replace an expired one.
type claimTarget struct {
	lease        ObjectStageWriteLease
	payloadState string
	attemptState string
	nonce        []byte
	checksum     []byte
}

// ClaimObjectStageWrite claims the current attempt or replaces an expired
// attempt with a new deterministic staging identity, under the payload row lock.
func (r *objectStageStore) ClaimObjectStageWrite(
	ctx context.Context,
	assignmentID, ledgerID, owner, token string,
	leaseDuration time.Duration,
) (ObjectStageWriteLease, bool, error) {
	leaseMicroseconds, err := validateClaimInput(assignmentID, ledgerID, owner, token, leaseDuration)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("begin object stage write claim: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	target, err := lockClaimTarget(ctx, tx, assignmentID, ledgerID)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	payloadUnexpired, leaseActive, err := claimClockState(ctx, tx, assignmentID, ledgerID, target.lease.StageAttemptID)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	if target.payloadState != "staging" || !payloadUnexpired {
		return ObjectStageWriteLease{}, false, ErrPayloadUnavailable
	}
	if len(target.checksum) != len(target.lease.CiphertextSHA256) {
		return ObjectStageWriteLease{}, false, errors.New("stored stage write checksum has invalid length")
	}

	lease := target.lease
	lease.Nonce = append([]byte(nil), target.nonce...)
	copy(lease.CiphertextSHA256[:], target.checksum)
	lease.Owner = owner
	lease.Token = token

	// Another writer holds an active lease; the acceptance is retryable.
	if target.attemptState == "leased" && leaseActive {
		if err := tx.Commit(ctx); err != nil {
			return ObjectStageWriteLease{}, false, fmt.Errorf("commit active object stage claim: %w", err)
		}
		return ObjectStageWriteLease{}, false, nil
	}

	if target.attemptState == "ready" {
		if lease.ExpiresAt, err = r.claimReadyAttempt(ctx, tx, assignmentID, lease.StageAttemptID, owner, token, leaseMicroseconds); err != nil {
			return ObjectStageWriteLease{}, false, err
		}
	} else if err := r.leaseReplacementAttempt(ctx, tx, &lease, owner, token, leaseMicroseconds); err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("commit object stage write claim: %w", err)
	}
	return lease, true, nil
}

// validateClaimInput validates the claim identifiers and lease duration.
func validateClaimInput(assignmentID, ledgerID, owner, token string, leaseDuration time.Duration) (int64, error) {
	for name, value := range map[string]string{
		"assignment ID": assignmentID,
		"ledger ID":     ledgerID,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return 0, err
		}
	}
	return boundedDurationMicroseconds("object stage write lease duration", leaseDuration, time.Hour, false)
}

// lockClaimTarget row-locks the object payload and its current stage-write
// attempt, returning the scanned identity for the claim decision.
func lockClaimTarget(ctx context.Context, tx pgx.Tx, assignmentID, ledgerID string) (claimTarget, error) {
	var t claimTarget
	err := tx.QueryRow(ctx, `
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
		&t.lease.AssignmentID,
		&t.lease.LedgerID,
		&t.lease.PayloadID,
		&t.payloadState,
		&t.lease.KeyReference,
		&t.lease.KeyVersion,
		&t.nonce,
		&t.checksum,
		&t.lease.CiphertextSize,
		&t.lease.StagingObjectKey,
		&t.lease.FinalObjectKey,
		&t.lease.StageAttemptID,
		&t.lease.Attempt,
		&t.attemptState,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return claimTarget{}, ErrNotFound
	}
	if err != nil {
		return claimTarget{}, fmt.Errorf("lock object stage write attempt: %w", err)
	}
	return t, nil
}

// claimClockState reports, from the database clock, whether the payload is
// unexpired and whether the target attempt still holds an active lease.
func claimClockState(ctx context.Context, tx pgx.Tx, assignmentID, ledgerID, attemptID string) (payloadUnexpired, leaseActive bool, err error) {
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
	`, assignmentID, ledgerID, attemptID).Scan(&payloadUnexpired, &leaseActive); err != nil {
		return false, false, fmt.Errorf("check object stage lease clock: %w", err)
	}
	return payloadUnexpired, leaseActive, nil
}

// leaseReplacementAttempt reclaims an expired attempt and leases a fresh
// replacement with new deterministic object keys, updating lease in place.
func (r *objectStageStore) leaseReplacementAttempt(ctx context.Context, tx pgx.Tx, lease *ObjectStageWriteLease, owner, token string, leaseMicros int64) error {
	assignmentID := lease.AssignmentID
	if err := r.orphanExpiredLeasedAttempt(ctx, tx, assignmentID, lease.StageAttemptID); err != nil {
		return err
	}
	nextAttempt := lease.Attempt + 1
	nextAttemptID, nextStagingKey, nextFinalKey, err := stageAttemptKeys(assignmentID, lease.PayloadID, nextAttempt)
	if err != nil {
		return err
	}
	expiresAt, err := r.insertLeasedAttempt(ctx, tx, leasedAttempt{
		assignmentID: assignmentID,
		ledgerID:     lease.LedgerID,
		attemptID:    nextAttemptID,
		attempt:      nextAttempt,
		stagingKey:   nextStagingKey,
		finalKey:     nextFinalKey,
		checksum:     lease.CiphertextSHA256[:],
		size:         lease.CiphertextSize,
		owner:        owner,
		token:        token,
		leaseMicros:  leaseMicros,
	})
	if err != nil {
		return err
	}
	if err := r.moveStagedIdentityKeys(ctx, tx, assignmentID, lease.LedgerID, nextStagingKey, nextFinalKey); err != nil {
		return err
	}
	lease.ExpiresAt = expiresAt
	lease.StageAttemptID = nextAttemptID
	lease.Attempt = nextAttempt
	lease.StagingObjectKey = nextStagingKey
	lease.FinalObjectKey = nextFinalKey
	return nil
}
