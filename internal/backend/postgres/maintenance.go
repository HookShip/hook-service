package postgres

import (
	"context"
	"errors"
	"time"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
)

// This file holds the retention and payload-reclaim surface of the PostgreSQL
// backend: thin delegations to the durability repository plus the bounded
// payload-deletion loop. Keeping them here keeps postgres.go focused on the
// runtime port implementation.

// PayloadBackendName reports which payload store is active for status output:
// "object-store" when the immutable object overlay is selected, else "postgres".
func (b *Backend) PayloadBackendName() string {
	if b.store != nil {
		return "object-store"
	}
	return "postgres"
}

// PurgeExpiredNonces deletes expired replay-ledger nonces up to limit rows,
// returning the number removed. It is safe to call repeatedly from a retention
// worker.
func (b *Backend) PurgeExpiredNonces(ctx context.Context, limit int) (int64, error) {
	return b.repo.PurgeExpiredNonces(ctx, limit)
}

// AdvanceTombstones transitions committed idempotency ledgers whose retention
// window elapsed into the tombstone state, freeing their payloads for deletion.
func (b *Backend) AdvanceTombstones(ctx context.Context, assignmentID string, limit int) (int64, error) {
	return b.repo.AdvanceTombstones(ctx, assignmentID, limit)
}

// MarkExpiredObjectStagesOrphaned orphans object-store payload stages that
// expired before acceptance so their ciphertext objects can be reclaimed.
func (b *Backend) MarkExpiredObjectStagesOrphaned(ctx context.Context, assignmentID string, limit int) (int64, error) {
	return b.repo.MarkExpiredObjectStagesOrphaned(ctx, assignmentID, limit)
}

// PurgeTerminalOutbox deletes published or dead metadata outbox rows older than
// olderThan, bounding growth of the payload-free metadata outbox.
func (b *Backend) PurgeTerminalOutbox(ctx context.Context, assignmentID string, olderThan time.Time, limit int) (int64, error) {
	return b.repo.PurgeTerminalOutbox(ctx, assignmentID, olderThan, limit)
}

// ReclaimExpiredPayloads claims and deletes eligible expired payloads for an
// assignment, removing object-store ciphertext (or nulling PostgreSQL ciphertext
// bytes) under a fencing lease. It returns the number reclaimed in this pass so
// the retention worker can drain in bounded batches.
func (b *Backend) ReclaimExpiredPayloads(ctx context.Context, assignmentID string, limit int) (int, error) {
	owner := b.ids.NewID("pdel")
	reclaimed := 0
	for i := 0; i < limit; i++ {
		token := b.ids.NewID("pdtk")
		lease, ok, err := b.repo.ClaimPayloadDeletion(ctx, assignmentID, owner, token, objectStageWriteLease)
		if err != nil {
			return reclaimed, err
		}
		if !ok {
			break
		}
		if b.store != nil {
			if err := b.deleteObjectPayloadObjects(ctx, lease); err != nil {
				// Leave the payload leased; the next pass reclaims it once the
				// lease expires. Never mark it deleted without removing the bytes.
				return reclaimed, err
			}
		}
		if err := b.repo.CompletePayloadDeletion(ctx, lease); err != nil {
			if errors.Is(err, durable.ErrLeaseLost) {
				continue
			}
			return reclaimed, err
		}
		reclaimed++
	}
	return reclaimed, nil
}
