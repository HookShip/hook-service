// Package memory implements the fully functional but intentionally ephemeral,
// evaluation-only native runtime profile. Every runtime port is satisfied with
// bounded, process-local state: an ingest that never survives a restart, real
// delivery with retries and a dead-letter queue, status, and a reset for test
// automation.
//
// It declares durability=ephemeral and never claims protocol-v1 durable
// acceptance, so ephemeral semantics cannot be mistaken for durable acceptance.
package memory

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/payloadcrypto"
	"github.com/hookship/hook-service/internal/runtime"
)

// Limits bounds every process-local collection so a hostile or runaway producer
// cannot exhaust memory. Reaching a limit returns runtime.ErrCapacity, which the
// ingest layer maps to retryable backpressure.
type Limits struct {
	MaxPayloadBytes int64
	MaxAssignments  int
	MaxLedgers      int
	MaxJobs         int
	MaxAttempts     int
	MaxOutbox       int
	MaxNonces       int
	TTL             time.Duration
}

// DefaultLimits returns conservative evaluation bounds.
func DefaultLimits() Limits {
	return Limits{
		MaxPayloadBytes: 1 << 20,
		MaxAssignments:  256,
		MaxLedgers:      50_000,
		MaxJobs:         50_000,
		MaxAttempts:     200_000,
		MaxOutbox:       50_000,
		MaxNonces:       100_000,
		TTL:             time.Hour,
	}
}

type ledgerEntry struct {
	ledgerID       string
	assignmentID   string
	idempotencyKey string
	fingerprint    runtime.Hash
	bodySHA256     runtime.Hash
	headers        runtime.StableHeaders
	state          string
	ingestID       string
	payloadID      string
	dispatchJobID  string
	reservedAt     time.Time
	acceptedAt     time.Time
	expiresAt      time.Time
}

type ingestRecord struct {
	assignmentID string
	headers      runtime.StableHeaders
	bodySHA256   runtime.Hash
	acceptedAt   time.Time
	expiresAt    time.Time
	updatedAt    time.Time
	payloadID    string
	dispatchJob  string
}

type payloadRecord struct {
	assignmentID string
	sealed       runtime.SealedPayload
	expiresAt    time.Time
}

type jobEntry struct {
	job         runtime.Job
	leaseOwner  string
	leaseToken  string
	leaseExpiry time.Time
	createdAt   time.Time
	completedAt time.Time
}

type outboxEntry struct {
	item        runtime.OutboxItem
	leaseOwner  string
	leaseToken  string
	leaseExpiry time.Time
	createdAt   time.Time
}

// Backend is the in-memory runtime profile.
type Backend struct {
	clock  runtime.Clock
	ids    runtime.IDSource
	sealer runtime.PayloadSealer
	source runtime.AssignmentSource
	limits Limits

	mu        sync.Mutex
	ledgers   map[string]*ledgerEntry // key: assignmentID\x00idempotencyKey
	nonces    map[string]time.Time    // key: credentialID\x00nonce -> retainUntil
	payloads  map[string]*payloadRecord
	ingests   map[string]*ingestRecord
	snapshots map[string]runtime.EndpointSnapshot
	endpoints map[string]runtime.EndpointConfig // key: snapshotID
	jobs      map[string]*jobEntry
	outbox    map[string]*outboxEntry
	attempts  []runtime.AttemptRecord
}

// New builds a memory backend from resolved assignments. Each assignment's
// endpoint is registered as an immutable snapshot and the assignment source is
// rewritten to reference it. The payload key is ephemeral and process-local.
func New(assignments []runtime.Assignment, clock runtime.Clock, ids runtime.IDSource, limits Limits) (*Backend, error) {
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	if ids == nil {
		ids = runtime.NewULIDSource(clock)
	}
	if len(assignments) == 0 {
		return nil, errors.New("memory backend requires at least one assignment")
	}
	if len(assignments) > limits.MaxAssignments {
		return nil, fmt.Errorf("memory backend supports at most %d assignments", limits.MaxAssignments)
	}

	key := make([]byte, payloadcrypto.KeySize)
	if _, err := readRandom(key); err != nil {
		return nil, err
	}
	sealer, err := payloadcrypto.NewSealer(key, "memory://evaluation/key/1", "1")
	if err != nil {
		return nil, err
	}

	backend := &Backend{
		clock:     clock,
		ids:       ids,
		sealer:    sealer,
		limits:    limits,
		ledgers:   make(map[string]*ledgerEntry),
		nonces:    make(map[string]time.Time),
		payloads:  make(map[string]*payloadRecord),
		ingests:   make(map[string]*ingestRecord),
		snapshots: make(map[string]runtime.EndpointSnapshot),
		endpoints: make(map[string]runtime.EndpointConfig),
		jobs:      make(map[string]*jobEntry),
		outbox:    make(map[string]*outboxEntry),
	}

	resolved := make([]runtime.Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		snapshot, err := backend.registerSnapshot(assignment)
		if err != nil {
			return nil, err
		}
		assignment.EndpointSnapshotID = snapshot.SnapshotID
		backend.endpoints[snapshot.SnapshotID] = assignment.Endpoint
		resolved = append(resolved, assignment)
	}
	store, err := assignmentconfig.NewStore(resolved)
	if err != nil {
		return nil, err
	}
	backend.source = store
	return backend, nil
}

func (b *Backend) registerSnapshot(assignment runtime.Assignment) (runtime.EndpointSnapshot, error) {
	content, err := json.Marshal(map[string]any{
		"endpoint": map[string]any{
			"id":               assignment.Endpoint.EndpointID,
			"url":              assignment.Endpoint.URL,
			"configGeneration": assignment.Endpoint.ConfigGeneration,
		},
	})
	if err != nil {
		return runtime.EndpointSnapshot{}, fmt.Errorf("marshal endpoint snapshot: %w", err)
	}
	hash := runtime.SHA256(content)
	snapshotID := "snap_" + hash.String()[:26]
	snapshot := runtime.EndpointSnapshot{
		SnapshotID:       snapshotID,
		AssignmentID:     assignment.ID,
		EndpointID:       assignment.Endpoint.EndpointID,
		BundleID:         assignment.Endpoint.BundleID,
		ConfigGeneration: assignment.Endpoint.ConfigGeneration,
		Content:          content,
		ContentSHA256:    hash,
		CreatedAt:        b.clock.Now(),
	}
	b.snapshots[snapshotID] = snapshot
	return snapshot, nil
}

// Name identifies the profile.
func (b *Backend) Name() string { return "memory" }

// Durable reports that the memory backend is never durable.
func (b *Backend) Durable() bool { return false }

// Ping always succeeds; the backend has no external dependency.
func (b *Backend) Ping(context.Context) error { return nil }

// Close releases nothing; state is garbage-collected.
func (b *Backend) Close() {}

// ResolveCredential delegates to the assignment source.
func (b *Backend) ResolveCredential(ctx context.Context, credentialID string) (runtime.Assignment, error) {
	return b.source.ResolveCredential(ctx, credentialID)
}

// Assignments delegates to the assignment source.
func (b *Backend) Assignments(ctx context.Context) ([]runtime.Assignment, error) {
	return b.source.Assignments(ctx)
}

// Seal encrypts a payload with the ephemeral process key.
func (b *Backend) Seal(plaintext []byte) (runtime.SealedPayload, error) {
	return b.sealer.Seal(plaintext)
}

// Open decrypts a sealed payload.
func (b *Backend) Open(sealed runtime.SealedPayload) ([]byte, error) {
	return b.sealer.Open(sealed)
}

// ReserveNonce records a first-seen nonce for replay protection within the
// retention window. It fails closed with ErrCapacity if the bound is reached.
func (b *Backend) ReserveNonce(_ context.Context, credentialID, nonce string, _ time.Time, retainUntil time.Time) (bool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	now := b.clock.Now()
	b.evictNoncesLocked(now)
	key := credentialID + "\x00" + nonce
	if until, ok := b.nonces[key]; ok && until.After(now) {
		return false, nil
	}
	if len(b.nonces) >= b.limits.MaxNonces {
		return false, runtime.ErrCapacity
	}
	if retainUntil.Before(now) {
		retainUntil = now.Add(b.limits.TTL)
	}
	b.nonces[key] = retainUntil
	return true, nil
}

func (b *Backend) evictNoncesLocked(now time.Time) {
	for key, until := range b.nonces {
		if !until.After(now) {
			delete(b.nonces, key)
		}
	}
}

func ledgerKey(assignmentID, idempotencyKey string) string {
	return assignmentID + "\x00" + idempotencyKey
}

func readRandom(destination []byte) (int, error) {
	return rand.Read(destination)
}

// Compile-time assertion that the memory backend satisfies the full runtime
// contract.
var _ runtime.Backend = (*Backend)(nil)
