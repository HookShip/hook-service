package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
)

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
	leaseMicroseconds, err := validateSupersedeInput(identity, owner, token, leaseDuration)
	if err != nil {
		return ObjectStageWriteLease{}, false, err
	}

	tx, err := r.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("begin object stage supersession: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	proceed, err := lockStagingForSupersede(ctx, tx, identity)
	if err != nil || !proceed {
		return ObjectStageWriteLease{}, false, err
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
	if err := applySupersededIdentity(ctx, tx, identity, nextStagingKey, nextFinalKey); err != nil {
		return ObjectStageWriteLease{}, false, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ObjectStageWriteLease{}, false, fmt.Errorf("commit object stage supersession: %w", err)
	}
	return newSupersedeLease(identity, nextAttemptID, nextAttempt, nextStagingKey, nextFinalKey, owner, token, leaseExpiresAt), true, nil
}

// validateSupersedeInput validates the new ciphertext identity and lease.
func validateSupersedeInput(identity ObjectPayloadStage, owner, token string, leaseDuration time.Duration) (int64, error) {
	for name, value := range map[string]string{
		"assignment ID": identity.AssignmentID,
		"ledger ID":     identity.LedgerID,
		"payload ID":    identity.PayloadID,
		"lease owner":   owner,
		"lease token":   token,
	} {
		if err := validateText(name, value, 255); err != nil {
			return 0, err
		}
	}
	if err := validateText("key reference", identity.KeyReference, 512); err != nil {
		return 0, err
	}
	if err := validateText("key version", identity.KeyVersion, 128); err != nil {
		return 0, err
	}
	if len(identity.Nonce) < 12 || len(identity.Nonce) > 32 {
		return 0, errors.New("ciphertext nonce must be between 12 and 32 bytes")
	}
	if identity.CiphertextSize <= 0 {
		return 0, errors.New("ciphertext size must be positive")
	}
	return boundedDurationMicroseconds("object stage write lease duration", leaseDuration, time.Hour, false)
}

// lockStagingForSupersede row-locks the payload and reports whether a
// supersession may proceed. It returns proceed=false with a nil error when the
// payload is not supersedable (finalized/committed/reclaimed — the caller resumes
// the durable object) and a sentinel error on expiry or a key-identity conflict.
func lockStagingForSupersede(ctx context.Context, tx pgx.Tx, identity ObjectPayloadStage) (bool, error) {
	var state, keyReference, keyVersion string
	var payloadUnexpired bool
	err := tx.QueryRow(ctx, `
		SELECT state::text, key_reference, key_version, expires_at > clock_timestamp()
		FROM payload_records
		WHERE assignment_id = $1 AND ledger_id = $2 AND payload_id = $3 AND backend = 'object_store'
		FOR UPDATE
	`, identity.AssignmentID, identity.LedgerID, identity.PayloadID).Scan(
		&state, &keyReference, &keyVersion, &payloadUnexpired,
	)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, ErrNotFound
	}
	if err != nil {
		return false, fmt.Errorf("lock object payload for supersession: %w", err)
	}
	// Never rebind a finalized or committed payload; the caller resumes the
	// durable object. Reclaimed/deleting payloads are also not supersedable.
	if state != "staging" {
		return false, nil
	}
	if !payloadUnexpired {
		return false, ErrPayloadUnavailable
	}
	// The data encryption key must be stable across the retry; a different key
	// reference/version is a genuine conflict, not a supersession.
	if keyReference != identity.KeyReference || keyVersion != identity.KeyVersion {
		return false, ErrPayloadConflict
	}
	return true, nil
}

// applySupersededIdentity moves the payload's staged identity to the new
// attempt. The relaxed payload guard permits this fenced ciphertext-identity
// change only because the caller has already inserted the matching leased
// attempt.
func applySupersededIdentity(ctx context.Context, tx pgx.Tx, identity ObjectPayloadStage, stagingKey, finalKey string) error {
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
		stagingKey,
		finalKey,
	)
	if err != nil {
		return fmt.Errorf("supersede object payload identity: %w", err)
	}
	if tag.RowsAffected() != 1 {
		return ErrPayloadUnavailable
	}
	return nil
}

// newSupersedeLease builds the write lease for the freshly leased attempt.
func newSupersedeLease(identity ObjectPayloadStage, attemptID string, attempt int, stagingKey, finalKey, owner, token string, leaseExpiresAt time.Time) ObjectStageWriteLease {
	return ObjectStageWriteLease{
		AssignmentID:     identity.AssignmentID,
		LedgerID:         identity.LedgerID,
		PayloadID:        identity.PayloadID,
		StageAttemptID:   attemptID,
		Attempt:          attempt,
		StagingObjectKey: stagingKey,
		FinalObjectKey:   finalKey,
		KeyReference:     identity.KeyReference,
		KeyVersion:       identity.KeyVersion,
		Nonce:            append([]byte(nil), identity.Nonce...),
		CiphertextSHA256: identity.CiphertextSHA256,
		CiphertextSize:   identity.CiphertextSize,
		Owner:            owner,
		Token:            token,
		ExpiresAt:        leaseExpiresAt,
	}
}
