package postgres

import (
	"errors"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// This file is the single, exhaustive place where PostgreSQL durability
// sentinels are translated into the runtime error contract. Two translation
// contexts exist and are intentionally distinct:
//
//   - translateError is the canonical mapping for read, status, redrive, and
//     lease-settle paths: each durability sentinel maps to the runtime sentinel
//     with the same meaning.
//   - translateAcceptanceError fails the atomic acceptance barrier closed: any
//     durability sentinel that means "the barrier could not be crossed cleanly"
//     becomes retryable runtime.ErrUnavailable, so the producer path never
//     returns a false 202.
//
// Every call site routes through one of these functions; no ad-hoc errors.Is
// chains are duplicated across the backend. The one deliberate exception is a
// payload-key mismatch during staging supersession, which is genuine corruption
// (not backpressure) and is surfaced as a hard error by its caller before the
// acceptance translation runs.

// translateError maps a durability sentinel to its natural runtime counterpart.
// Unknown (already-wrapped or infrastructure) errors pass through unchanged, and
// nil maps to nil so callers can translate unconditionally.
func translateError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, durable.ErrNotFound):
		return runtime.ErrNotFound
	case errors.Is(err, durable.ErrLeaseLost):
		return runtime.ErrLeaseLost
	case errors.Is(err, durable.ErrNotPending):
		return runtime.ErrUnavailable
	case errors.Is(err, durable.ErrPayloadConflict):
		return runtime.ErrConflict
	case errors.Is(err, durable.ErrSnapshotConflict):
		return runtime.ErrConflict
	case errors.Is(err, durable.ErrPayloadUnavailable):
		return runtime.ErrPayloadObjectNotFound
	default:
		return err
	}
}

// translateAcceptanceError collapses every durability sentinel that can block
// the atomic acceptance barrier into retryable runtime.ErrUnavailable, so a
// transient or racing barrier failure is retried rather than mistaken for a
// false success. Non-durability errors pass through unchanged, and nil maps to
// nil.
func translateAcceptanceError(err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, durable.ErrNotPending),
		errors.Is(err, durable.ErrNotFound),
		errors.Is(err, durable.ErrPayloadConflict),
		errors.Is(err, durable.ErrPayloadUnavailable),
		errors.Is(err, durable.ErrSnapshotConflict),
		errors.Is(err, durable.ErrLeaseLost):
		return runtime.ErrUnavailable
	default:
		return err
	}
}
