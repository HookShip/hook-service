package postgres

import (
	"errors"

	"github.com/jackc/pgx/v5/pgxpool"
)

// base carries the shared PostgreSQL connection pool for every durability
// component. It is embedded (unexported) so components share connection handling
// without exposing it, and it is the single home for cross-component pool/tx
// helpers.
type base struct {
	pool *pgxpool.Pool
}

// The single-cell durability schema is owned by focused components, each
// responsible for one cohesive slice of the model. Keeping them small makes the
// state machines and transaction ownership easy to review.
type (
	// idempotencyStore owns the idempotency ledger: reservation, resolution, and
	// the commit barrier bookkeeping.
	idempotencyStore struct{ base }
	// acceptanceStore owns the PostgreSQL acceptance barrier: endpoint snapshots,
	// immutable ingest, committed payload ciphertext, and ingest status reads.
	acceptanceStore struct{ base }
	// objectStageStore owns the object-store payload staging state machine and
	// its committed object locator reads.
	objectStageStore struct{ base }
	// jobQueue owns durable dispatch jobs claimed under fencing leases and the
	// immutable attempt/outbox write.
	jobQueue struct{ base }
	// outboxQueue owns the metadata outbox drain lifecycle.
	outboxQueue struct{ base }
	// nonceLedger owns replay-protection nonces and their expiry purge.
	nonceLedger struct{ base }
	// diagnostics owns operator visibility and explicit redrive over dead work.
	diagnostics struct{ base }
	// retentionManager owns retention transitions: tombstones, orphan marking,
	// terminal-outbox purge, and fenced payload deletion.
	retentionManager struct{ base }
)

// Repository is a thin facade composing the focused durability components. It
// embeds each component so the public method set (and therefore every caller)
// is unchanged, while the implementation lives in small single-responsibility
// files.
type Repository struct {
	*idempotencyStore
	*acceptanceStore
	*objectStageStore
	*jobQueue
	*outboxQueue
	*nonceLedger
	*diagnostics
	*retentionManager
}

// New builds a Repository over an existing pool, wiring every component to the
// same shared connection base.
func New(pool *pgxpool.Pool) (*Repository, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	shared := base{pool: pool}
	return &Repository{
		idempotencyStore: &idempotencyStore{shared},
		acceptanceStore:  &acceptanceStore{shared},
		objectStageStore: &objectStageStore{shared},
		jobQueue:         &jobQueue{shared},
		outboxQueue:      &outboxQueue{shared},
		nonceLedger:      &nonceLedger{shared},
		diagnostics:      &diagnostics{shared},
		retentionManager: &retentionManager{shared},
	}, nil
}
