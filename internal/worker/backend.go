package worker

import (
	"context"

	"github.com/hookship/hook-service/internal/runtime"
)

// Backend is the minimal set of runtime ports the delivery worker depends on: it
// lists assignments for fair iteration, drives the durable dispatch queue (claim
// under a fencing lease, load the committed delivery, and settle or record the
// outcome), and drains the payload-free metadata outbox. It is intentionally
// narrower than runtime.Backend (interface segregation): the worker never
// resolves credentials, seals payloads, or reads ingest status, so those ports
// are deliberately absent. Every concrete profile satisfies this interface
// structurally.
type Backend interface {
	// Assignments lists configured assignments for fair, per-assignment
	// iteration (the listing half of runtime.AssignmentSource).
	Assignments(ctx context.Context) ([]runtime.Assignment, error)

	// Queue owns durable dispatch jobs claimed under fencing leases plus the
	// immutable attempt and metadata-outbox writes.
	runtime.Queue

	// OutboxQueue owns the metadata outbox drain lifecycle.
	runtime.OutboxQueue
}

// The full runtime.Backend must always satisfy the narrow worker.Backend, so
// switching the worker to the segregated interface can never reject a concrete
// profile.
var _ Backend = (runtime.Backend)(nil)
