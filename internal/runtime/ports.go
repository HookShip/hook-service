package runtime

import (
	"context"
	"errors"
	"time"

	"github.com/hookship/hook-service/internal/idempotency"
)

// Sentinel errors mapped by the ingest and worker layers to protocol behavior.
var (
	// ErrNotFound reports a missing durable record (e.g. unknown ingest ID).
	ErrNotFound = errors.New("runtime: record not found")
	// ErrLeaseLost reports that a job or outbox lease was fenced or expired, so
	// the caller must not settle the work.
	ErrLeaseLost = errors.New("runtime: lease lost or fenced")
	// ErrConflict reports an idempotency body-hash conflict.
	ErrConflict = errors.New("runtime: idempotency conflict")
	// ErrCapacity reports that a bounded ephemeral backend rejected work because
	// a strict limit was reached. It maps to retryable backpressure.
	ErrCapacity = errors.New("runtime: bounded capacity exhausted")
	// ErrUnavailable reports that the acceptance barrier could not be crossed and
	// the caller must return retryable 503 without a false success.
	ErrUnavailable = errors.New("runtime: durable acceptance unavailable")
	// ErrPayloadObjectNotFound reports that an immutable ciphertext object is
	// absent from the PayloadStore. It is distinct from ErrNotFound so the
	// delivery path can tell a missing object from a missing durable record.
	ErrPayloadObjectNotFound = errors.New("runtime: payload object not found")
	// ErrPayloadObjectConflict reports that an immutable object key already holds
	// different bytes than the caller intended to write. Immutable keys are never
	// overwritten, so this is always a caller or corruption error, never retried
	// as backpressure.
	ErrPayloadObjectConflict = errors.New("runtime: payload object immutability conflict")
)

// Job kinds used across the durable queue. The single-cell PostgreSQL model
// records dispatch work under this kind; every backend uses the same value so
// worker claims are identical across profiles.
const JobKindDispatch = "dispatch"

// RequestFingerprint binds the exact body hash and every stable signed header.
// Replay-only timestamp, nonce, signature, and credential ID are excluded so a
// legitimate retry with fresh replay fields matches. It delegates to the shared
// idempotency package so it is byte-identical to the PostgreSQL durability
// fingerprint and canonical identities agree across profiles.
func RequestFingerprint(bodySHA256 Hash, headers StableHeaders) Hash {
	return Hash(idempotency.Fingerprint(bodySHA256, headers))
}

// AssignmentSource resolves a verified credential to its immutable assignment
// and lists assignments for worker fairness iteration. Implementations never
// enter the delivery hot path once an assignment is loaded.
type AssignmentSource interface {
	// ResolveCredential returns the assignment bound to a credential ID. A
	// missing credential returns ErrNotFound with no existence disclosure at the
	// protocol layer.
	ResolveCredential(ctx context.Context, credentialID string) (Assignment, error)
	// Assignments lists every configured assignment for worker iteration.
	Assignments(ctx context.Context) ([]Assignment, error)
}

// NonceLedger provides replay protection over (credentialID, nonce).
type NonceLedger interface {
	// ReserveNonce records a first-seen nonce, returning false if it was already
	// observed within the retention window.
	ReserveNonce(ctx context.Context, credentialID, nonce string, signedAt, retainUntil time.Time) (bool, error)
}

// PayloadSealer encrypts and decrypts producer payloads. The plaintext body is
// never persisted; only the sealed ciphertext crosses the acceptance barrier.
type PayloadSealer interface {
	Seal(plaintext []byte) (SealedPayload, error)
	Open(sealed SealedPayload) ([]byte, error)
}

// ObjectPutResult reports the outcome of an immutable object PUT.
type ObjectPutResult struct {
	// Version is the provider-assigned version identifier (ETag or version id)
	// for the written object. It may be empty when the store is unversioned; the
	// object-store orchestration then substitutes a checksum-derived token so the
	// durable ledger always records a non-empty provenance version.
	Version string
	// AlreadyExisted reports that an identical object was already present, so the
	// PUT was a verified idempotent no-op rather than a first write.
	AlreadyExisted bool
}

// PayloadStore is the immutable ciphertext object sink used by the object-store
// payload backend. It is deliberately orthogonal to PayloadSealer: the sealer
// turns plaintext into ciphertext and metadata; the store persists that opaque
// ciphertext under a deterministic, payload-free object key. Plaintext never
// reaches a PayloadStore, and the durable PostgreSQL ledger keeps only the
// object locator, checksum, and key metadata — never the bytes. Implementations
// must be safe for concurrent use.
type PayloadStore interface {
	// PutImmutable writes ciphertext to key. It is idempotent for identical
	// bytes: writing the same checksum to the same key succeeds and reports
	// AlreadyExisted without overwriting. Writing different bytes to an existing
	// key must fail with ErrPayloadObjectConflict and never overwrite. checksum
	// is the SHA-256 of data and is verified end to end.
	PutImmutable(ctx context.Context, key string, data []byte, checksum Hash) (ObjectPutResult, error)
	// Get reads and verifies the ciphertext at key. A missing object returns
	// ErrPayloadObjectNotFound; a checksum mismatch returns a non-nil error and
	// never returns unverified bytes.
	Get(ctx context.Context, key string, checksum Hash) ([]byte, error)
	// Delete removes the object at key. It is idempotent: deleting a missing
	// object returns nil so orphan cleanup can run repeatedly.
	Delete(ctx context.Context, key string) error
	// Ping verifies the store and its bucket are reachable for readiness probes.
	Ping(ctx context.Context) error
	// Name identifies the store implementation for status and metrics.
	Name() string
}

// IngestStore owns the acceptance barrier and status reads.
type IngestStore interface {
	// ReserveIdempotency reserves or resolves an idempotency identity.
	ReserveIdempotency(ctx context.Context, request ReservationRequest) (ReservationResult, error)
	// Accept crosses the durability acknowledgement barrier atomically.
	Accept(ctx context.Context, request AcceptanceRequest) error
	// Status returns the payload-free status of an ingest owned by the
	// assignment. Foreign and unknown IDs are indistinguishable (ErrNotFound).
	Status(ctx context.Context, assignmentID, ingestID string) (IngestStatus, error)
}

// Queue owns durable dispatch jobs claimed under fencing leases.
type Queue interface {
	// ClaimJob claims one ready job for an assignment under a fencing lease.
	ClaimJob(ctx context.Context, assignmentID, kind, owner, token string, lease time.Duration) (Job, bool, error)
	// LoadDelivery loads the committed delivery context for a claimed job. The
	// returned Body is decrypted plaintext held only in worker memory.
	LoadDelivery(ctx context.Context, job Job) (DeliveryContext, error)
	// CompleteJob settles a successful job; the lease must still be held.
	CompleteJob(ctx context.Context, lease JobLease) error
	// RetryJob reschedules a job after a delay, moving it to dead when attempts
	// are exhausted.
	RetryJob(ctx context.Context, lease JobLease, delay time.Duration, errorCode string) error
	// FailJob terminally dead-letters a job under lease regardless of remaining
	// attempts, for non-retryable (terminal policy) outcomes.
	FailJob(ctx context.Context, lease JobLease, errorCode string) error
	// RecordAttempt writes the immutable attempt and its metadata outbox row.
	RecordAttempt(ctx context.Context, attempt AttemptRecord, observation OutboxObservation) error
}

// OutboxQueue owns the metadata outbox delivery lifecycle.
type OutboxQueue interface {
	ClaimOutbox(ctx context.Context, assignmentID, owner, token string, lease time.Duration) (OutboxItem, bool, error)
	PublishOutbox(ctx context.Context, lease OutboxLease) error
	RetryOutbox(ctx context.Context, lease OutboxLease, delay time.Duration, errorCode string) error
}

// DiagnosticsQueue exposes operator visibility and redrive actions over durable
// dead work. Redrive is always explicit and never silent mutation.
type DiagnosticsQueue interface {
	// ListDeadJobs returns terminal dead jobs for an assignment.
	ListDeadJobs(ctx context.Context, assignmentID string, limit int) ([]Job, error)
	// RedriveJob moves a dead job back to ready as an explicit operator action.
	RedriveJob(ctx context.Context, assignmentID, jobID string, runAt time.Time) error
}

// Backend is one fully assembled native runtime profile. It bundles every port
// plus lifecycle and self-description. The ingest handler and worker depend only
// on this interface.
type Backend interface {
	AssignmentSource
	NonceLedger
	PayloadSealer
	IngestStore
	Queue
	OutboxQueue
	DiagnosticsQueue

	// Name is the profile identifier (memory, postgres, postgres-redis, ...).
	Name() string
	// Durable reports whether accepted events survive a restart. Only durable
	// backends may return protocol-v1 202 acceptance.
	Durable() bool
	// Ping verifies backend liveness for readiness probes.
	Ping(ctx context.Context) error
	// Close releases backend resources.
	Close()
}
