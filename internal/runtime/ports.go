package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"time"
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
)

// Job kinds used across the durable queue. The single-cell PostgreSQL model
// records dispatch work under this kind; every backend uses the same value so
// worker claims are identical across profiles.
const JobKindDispatch = "dispatch"

// RequestFingerprint binds the exact body hash and every stable signed header.
// Replay-only timestamp, nonce, signature, and credential ID are excluded so a
// legitimate retry with fresh replay fields matches. It is byte-identical to the
// PostgreSQL durability fingerprint so canonical identities agree across
// profiles.
func RequestFingerprint(bodySHA256 Hash, headers StableHeaders) Hash {
	digest := sha256.New()
	_, _ = digest.Write([]byte("hookship-idempotency-fingerprint-v1\x00"))
	_, _ = digest.Write(bodySHA256[:])
	for _, value := range []string{
		headers.EventID,
		headers.EventType,
		headers.Route,
		headers.ContentType,
	} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	var result Hash
	copy(result[:], digest.Sum(nil))
	return result
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
