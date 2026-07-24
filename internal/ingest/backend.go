package ingest

import (
	"context"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// Backend is the minimal set of runtime ports the protocol-v1 producer data
// plane depends on. It is intentionally narrower than runtime.Backend (interface
// segregation): the ingest handler authorizes a credential, protects against
// nonce replay, seals the body, reserves and accepts across the durability
// barrier, and reads payload-free status — nothing else. It never claims jobs,
// loads deliveries, or drives the outbox, so those ports are deliberately
// absent. Every concrete profile satisfies this interface structurally, and a
// test double needs to implement only these methods.
type Backend interface {
	// ResolveCredential authorizes a verified credential to its immutable
	// assignment (the credential-resolving half of runtime.AssignmentSource).
	ResolveCredential(ctx context.Context, credentialID string) (runtime.Assignment, error)

	// ReserveNonce provides replay protection over (credentialID, nonce).
	ReserveNonce(ctx context.Context, credentialID, nonce string, signedAt, retainUntil time.Time) (bool, error)

	// Seal encrypts the producer body before it crosses the acceptance barrier
	// (the sealing half of runtime.PayloadSealer; ingest never opens payloads).
	Seal(plaintext []byte) (runtime.SealedPayload, error)

	// IngestStore owns idempotency reservation, the acceptance barrier, and the
	// payload-free status read.
	runtime.IngestStore

	// Durable reports whether the profile may return protocol-v1 202 acceptance.
	Durable() bool
}

// The full runtime.Backend must always satisfy the narrow ingest.Backend, so
// switching the handler to the segregated interface can never reject a concrete
// profile.
var _ Backend = (runtime.Backend)(nil)
