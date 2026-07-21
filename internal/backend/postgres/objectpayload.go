package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// objectStageWriteLease bounds how long a single acceptance holds the durable
// stage-write lease while it PUTs ciphertext to the object store. It is short
// enough that a crashed writer is reclaimed promptly and well under the
// repository's one-hour lease ceiling.
const objectStageWriteLease = 5 * time.Minute

// acceptObject crosses the protocol-v1 acceptance barrier for the object-store
// payload backend. The barrier requires a successful immutable object PUT *and*
// the durable PostgreSQL acceptance/job commit: the ciphertext is written to the
// store and finalized before AcceptFinalizedObject commits the ingest, payload,
// dispatch job, and ledger atomically. A failure anywhere returns a retryable
// error and never a false acceptance; a crash may leave a recoverable orphan
// ciphertext object, cleaned up by the retention worker.
func (b *Backend) acceptObject(ctx context.Context, request runtime.AcceptanceRequest) error {
	assignmentID := request.Assignment.ID
	ledgerID := request.LedgerID
	payloadExpiry := request.PayloadExpiresAt
	if payloadExpiry.IsZero() {
		payloadExpiry = request.AcceptedAt.Add(durable.MinimumRetryHorizon)
	}

	stage, err := b.repo.GetObjectPayloadStage(ctx, assignmentID, ledgerID)
	switch {
	case errors.Is(err, durable.ErrNotFound):
		// First attempt for this ledger: record the object-store staging intent
		// with the freshly sealed identity. The ciphertext never touches the
		// database; only its locator, checksum, and key metadata are stored.
		stageErr := b.repo.StageObjectPayload(ctx, durable.ObjectPayloadStage{
			AssignmentID:     assignmentID,
			LedgerID:         ledgerID,
			PayloadID:        request.PayloadID,
			KeyReference:     request.Payload.KeyReference,
			KeyVersion:       request.Payload.KeyVersion,
			Nonce:            request.Payload.Nonce,
			CiphertextSHA256: durable.Hash(request.Payload.CiphertextSHA256),
			CiphertextSize:   int64(len(request.Payload.Ciphertext)),
			ExpiresAt:        payloadExpiry,
			StagedAt:         request.AcceptedAt,
		})
		if mapped := mapObjectStageError(stageErr); mapped != nil {
			return mapped
		}
		stage, err = b.repo.GetObjectPayloadStage(ctx, assignmentID, ledgerID)
		if err != nil {
			return err
		}
	case err != nil:
		return err
	}

	switch stage.State {
	case "committed":
		// The acceptance barrier was already crossed for this ledger; treat a
		// duplicate accept as an idempotent success.
		return nil
	case "orphaned", "deleting", "deleted":
		// The staged ciphertext was reclaimed; the caller must retry with a fresh
		// idempotency identity once retention clears the pending reservation.
		return runtime.ErrUnavailable
	case "staging":
		if durable.Hash(request.Payload.CiphertextSHA256) != stage.CiphertextSHA256 {
			// A prior attempt staged a different ciphertext identity for this
			// ledger and we cannot reproduce those exact bytes. Fail closed and
			// let retention orphan and delete the recoverable staged object.
			return runtime.ErrUnavailable
		}
		if err := b.writeStagedObject(ctx, assignmentID, ledgerID, request.Payload.Ciphertext); err != nil {
			return err
		}
	case "finalized":
		// The object is already durable in the store (a crash between finalize
		// and commit); skip the re-write and cross the barrier directly.
	default:
		return fmt.Errorf("unexpected object payload state %q", stage.State)
	}

	acceptance := durable.ObjectAcceptance{
		AssignmentID:        assignmentID,
		IdempotencyKey:      request.IdempotencyKey,
		LedgerID:            ledgerID,
		IngestID:            request.IngestID,
		PayloadID:           stage.PayloadID,
		DeliveryID:          request.DeliveryID,
		DispatchJobID:       request.DispatchJobID,
		EndpointSnapshotID:  request.EndpointSnapshotID,
		AcceptedAt:          request.AcceptedAt,
		IdempotencyExpires:  request.IdempotencyExpires,
		DispatchRunAt:       request.DispatchRunAt,
		DispatchMaxAttempts: request.DispatchMaxAttempts,
	}
	if err := b.repo.AcceptFinalizedObject(ctx, acceptance); err != nil {
		if errors.Is(err, durable.ErrNotPending) || errors.Is(err, durable.ErrNotFound) {
			return runtime.ErrUnavailable
		}
		return err
	}
	return nil
}

// writeStagedObject claims the durable stage-write lease, PUTs the ciphertext to
// the immutable staging and final object keys, and records the promotion so the
// payload becomes finalized. Every object write is checksum-verified by the
// store; the lease fences the finalize so a stale writer can never finalize.
func (b *Backend) writeStagedObject(ctx context.Context, assignmentID, ledgerID string, ciphertext []byte) error {
	owner := b.ids.NewID("objw")
	token := b.ids.NewID("objt")
	lease, ok, err := b.repo.ClaimObjectStageWrite(ctx, assignmentID, ledgerID, owner, token, objectStageWriteLease)
	if err != nil {
		if errors.Is(err, durable.ErrPayloadUnavailable) || errors.Is(err, durable.ErrNotFound) {
			return runtime.ErrUnavailable
		}
		return err
	}
	if !ok {
		// Another writer holds an active lease; the acceptance is retryable.
		return runtime.ErrUnavailable
	}

	checksum := runtime.Hash(lease.CiphertextSHA256)
	stagingResult, err := b.store.PutImmutable(ctx, lease.StagingObjectKey, ciphertext, checksum)
	if err != nil {
		return mapObjectStoreError("stage object put", err)
	}
	finalResult, err := b.store.PutImmutable(ctx, lease.FinalObjectKey, ciphertext, checksum)
	if err != nil {
		return mapObjectStoreError("final object put", err)
	}

	mode := "create_only"
	if finalResult.AlreadyExisted {
		mode = "verified_existing"
	}
	promotion := durable.ObjectPromotion{
		ProviderStagingVersion: providerVersion(stagingResult.Version, checksum),
		ProviderFinalVersion:   providerVersion(finalResult.Version, checksum),
		Mode:                   mode,
	}
	if err := b.repo.FinalizeObjectPayload(ctx, lease, promotion); err != nil {
		if errors.Is(err, durable.ErrLeaseLost) {
			return runtime.ErrUnavailable
		}
		return err
	}
	return nil
}

// loadObjectDelivery loads a committed object-store payload for delivery by
// reading the payload-free locator from PostgreSQL and fetching the immutable
// ciphertext from the object store. It is restart-safe: any replica can serve a
// claimed job without depending on the process that accepted it.
func (b *Backend) loadObjectDelivery(ctx context.Context, job runtime.Job) (runtime.DeliveryContext, error) {
	endpoint, ok := b.endpoints[job.EndpointSnapshotID]
	if !ok {
		return runtime.DeliveryContext{}, runtime.ErrNotFound
	}
	locator, err := b.repo.LoadObjectPayloadLocator(ctx, job.AssignmentID, job.PayloadID)
	if errors.Is(err, durable.ErrNotFound) {
		return runtime.DeliveryContext{}, runtime.ErrNotFound
	}
	if err != nil {
		return runtime.DeliveryContext{}, err
	}
	status, err := b.repo.LoadIngestStatus(ctx, job.AssignmentID, job.IngestID)
	if errors.Is(err, durable.ErrNotFound) {
		return runtime.DeliveryContext{}, runtime.ErrNotFound
	}
	if err != nil {
		return runtime.DeliveryContext{}, err
	}
	ciphertext, err := b.store.Get(ctx, locator.FinalObjectKey, runtime.Hash(locator.CiphertextSHA256))
	if err != nil {
		return runtime.DeliveryContext{}, mapObjectStoreError("get object ciphertext", err)
	}
	plaintext, err := b.sealer.Open(runtime.SealedPayload{
		Ciphertext:       ciphertext,
		Nonce:            locator.Nonce,
		KeyReference:     locator.KeyReference,
		KeyVersion:       locator.KeyVersion,
		CiphertextSHA256: runtime.Hash(locator.CiphertextSHA256),
	})
	if err != nil {
		return runtime.DeliveryContext{}, err
	}
	return runtime.DeliveryContext{
		Job:          job,
		Endpoint:     endpoint,
		EventID:      status.EventID,
		EventType:    status.EventType,
		ContentType:  status.ContentType,
		Body:         plaintext,
		BodySHA256:   runtime.Hash(status.BodySHA256),
		AssignmentID: job.AssignmentID,
	}, nil
}

// deleteObjectPayloadObjects removes every immutable object written for a
// payload under deletion, including stale stage-write attempts, so orphan
// cleanup leaves no ciphertext behind. It is safe to call repeatedly.
func (b *Backend) deleteObjectPayloadObjects(ctx context.Context, lease durable.PayloadDeletionLease) error {
	if lease.Backend != "object_store" {
		return nil
	}
	attempts, err := b.repo.ListObjectStageCleanup(ctx, lease.AssignmentID, lease.PayloadID)
	if err != nil {
		return err
	}
	keys := make(map[string]struct{})
	if lease.StagingObjectKey != "" {
		keys[lease.StagingObjectKey] = struct{}{}
	}
	if lease.FinalObjectKey != "" {
		keys[lease.FinalObjectKey] = struct{}{}
	}
	for _, attempt := range attempts {
		if attempt.StagingObjectKey != "" {
			keys[attempt.StagingObjectKey] = struct{}{}
		}
		if attempt.FinalObjectKey != "" {
			keys[attempt.FinalObjectKey] = struct{}{}
		}
	}
	for key := range keys {
		if err := b.store.Delete(ctx, key); err != nil {
			return mapObjectStoreError("delete object ciphertext", err)
		}
	}
	return nil
}

// providerVersion returns the store-reported version or, when the store is
// unversioned, a deterministic checksum-derived token so the durable ledger
// always records a non-empty provenance version.
func providerVersion(version string, checksum runtime.Hash) string {
	if version != "" {
		return version
	}
	return "sha256-" + checksum.String()
}

// mapObjectStageError maps staging conflicts to retryable unavailability so the
// acceptance path never returns a false success.
func mapObjectStageError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, durable.ErrNotPending),
		errors.Is(err, durable.ErrPayloadConflict),
		errors.Is(err, durable.ErrPayloadUnavailable),
		errors.Is(err, durable.ErrNotFound):
		return runtime.ErrUnavailable
	default:
		return err
	}
}

// mapObjectStoreError classifies object-store failures. A hard immutability
// conflict is surfaced as-is (it indicates corruption, not backpressure); every
// other transient failure becomes retryable unavailability so no false
// acceptance is returned.
func mapObjectStoreError(op string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, runtime.ErrPayloadObjectConflict) {
		return fmt.Errorf("%s: %w", op, err)
	}
	return runtime.ErrUnavailable
}
