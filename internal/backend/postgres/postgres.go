// Package postgres assembles the durable single-cell PostgreSQL runtime profile
// from the reviewed durability repository. PostgreSQL is the source of truth and
// the protocol-v1 acceptance barrier: ingest, ledger, encrypted payload, and
// ready dispatch job all commit atomically. Payloads are encrypted before they
// reach the database and decrypted only inside a worker operation.
package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/runtime"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

// Backend is the durable PostgreSQL runtime profile.
type Backend struct {
	pool      *pgxpool.Pool
	repo      *durable.Repository
	sealer    runtime.PayloadSealer
	source    runtime.AssignmentSource
	endpoints map[string]runtime.EndpointConfig
	clock     runtime.Clock
	ownsPool  bool
}

// New builds a PostgreSQL backend over an existing pool, registering each
// assignment's immutable endpoint snapshot and rewriting the assignment source
// to reference it.
func New(
	ctx context.Context,
	pool *pgxpool.Pool,
	assignments []runtime.Assignment,
	sealer runtime.PayloadSealer,
	clock runtime.Clock,
	ownsPool bool,
) (*Backend, error) {
	if pool == nil {
		return nil, errors.New("postgres pool is required")
	}
	if sealer == nil {
		return nil, errors.New("payload sealer is required")
	}
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	repo, err := durable.New(pool)
	if err != nil {
		return nil, err
	}
	backend := &Backend{
		pool:      pool,
		repo:      repo,
		sealer:    sealer,
		endpoints: make(map[string]runtime.EndpointConfig),
		clock:     clock,
		ownsPool:  ownsPool,
	}

	resolved := make([]runtime.Assignment, 0, len(assignments))
	for _, assignment := range assignments {
		snapshotID, err := backend.registerSnapshot(ctx, assignment)
		if err != nil {
			return nil, err
		}
		assignment.EndpointSnapshotID = snapshotID
		backend.endpoints[snapshotID] = assignment.Endpoint
		resolved = append(resolved, assignment)
	}
	store, err := assignmentconfig.NewStore(resolved)
	if err != nil {
		return nil, err
	}
	backend.source = store
	return backend, nil
}

func (b *Backend) registerSnapshot(ctx context.Context, assignment runtime.Assignment) (string, error) {
	content, err := json.Marshal(map[string]any{
		"endpoint": map[string]any{
			"id":               assignment.Endpoint.EndpointID,
			"url":              assignment.Endpoint.URL,
			"configGeneration": assignment.Endpoint.ConfigGeneration,
		},
	})
	if err != nil {
		return "", fmt.Errorf("marshal endpoint snapshot: %w", err)
	}
	hash := runtime.SHA256(content)
	snapshotID := "snap_" + hash.String()[:26]
	if _, err := b.repo.PutEndpointSnapshot(ctx, durable.EndpointSnapshot{
		SnapshotID:       snapshotID,
		AssignmentID:     assignment.ID,
		EndpointID:       assignment.Endpoint.EndpointID,
		BundleID:         assignment.Endpoint.BundleID,
		ConfigGeneration: assignment.Endpoint.ConfigGeneration,
		Content:          content,
		ContentSHA256:    durable.Hash(hash),
		CreatedAt:        b.clock.Now(),
	}); err != nil {
		return "", fmt.Errorf("register endpoint snapshot: %w", err)
	}
	return snapshotID, nil
}

// Name identifies the profile.
func (b *Backend) Name() string { return "postgres" }

// Durable reports that accepted events survive a restart.
func (b *Backend) Durable() bool { return true }

// Ping verifies database liveness.
func (b *Backend) Ping(ctx context.Context) error { return b.pool.Ping(ctx) }

// Close releases the pool if this backend owns it.
func (b *Backend) Close() {
	if b.ownsPool && b.pool != nil {
		b.pool.Close()
	}
}

// ResolveCredential delegates to the assignment source.
func (b *Backend) ResolveCredential(ctx context.Context, credentialID string) (runtime.Assignment, error) {
	return b.source.ResolveCredential(ctx, credentialID)
}

// Assignments delegates to the assignment source.
func (b *Backend) Assignments(ctx context.Context) ([]runtime.Assignment, error) {
	return b.source.Assignments(ctx)
}

// Seal encrypts a payload.
func (b *Backend) Seal(plaintext []byte) (runtime.SealedPayload, error) {
	return b.sealer.Seal(plaintext)
}

// Open decrypts a sealed payload.
func (b *Backend) Open(sealed runtime.SealedPayload) ([]byte, error) {
	return b.sealer.Open(sealed)
}

// ReserveNonce delegates durable replay protection to the repository.
func (b *Backend) ReserveNonce(ctx context.Context, credentialID, nonce string, signedAt, retainUntil time.Time) (bool, error) {
	return b.repo.ReserveNonce(ctx, credentialID, nonce, signedAt, retainUntil)
}

// ReserveIdempotency reserves or resolves an idempotency identity durably.
func (b *Backend) ReserveIdempotency(ctx context.Context, request runtime.ReservationRequest) (runtime.ReservationResult, error) {
	result, err := b.repo.ReserveIdempotency(ctx, durable.ReservationRequest{
		LedgerID:       request.LedgerID,
		AssignmentID:   request.AssignmentID,
		IdempotencyKey: request.IdempotencyKey,
		BodySHA256:     durable.Hash(request.BodySHA256),
		Headers:        durable.StableHeaders(request.Headers),
		ReservedAt:     request.ReservedAt,
	})
	if err != nil {
		return runtime.ReservationResult{}, err
	}
	return runtime.ReservationResult{
		Status:      runtime.ReservationStatus(result.Status),
		Created:     result.Created,
		Reservation: toRuntimeReservation(result.Reservation),
	}, nil
}

// Accept crosses the durable acceptance barrier atomically.
func (b *Backend) Accept(ctx context.Context, request runtime.AcceptanceRequest) error {
	payloadExpiry := request.PayloadExpiresAt
	if payloadExpiry.IsZero() {
		payloadExpiry = request.AcceptedAt.Add(durable.MinimumRetryHorizon)
	}
	err := b.repo.AcceptPostgres(ctx, durable.PostgresAcceptance{
		AssignmentID:        request.Assignment.ID,
		IdempotencyKey:      request.IdempotencyKey,
		LedgerID:            request.LedgerID,
		IngestID:            request.IngestID,
		PayloadID:           request.PayloadID,
		DeliveryID:          request.DeliveryID,
		DispatchJobID:       request.DispatchJobID,
		EndpointSnapshotID:  request.EndpointSnapshotID,
		Ciphertext:          request.Payload.Ciphertext,
		KeyReference:        request.Payload.KeyReference,
		KeyVersion:          request.Payload.KeyVersion,
		Nonce:               request.Payload.Nonce,
		CiphertextSHA256:    durable.Hash(request.Payload.CiphertextSHA256),
		PayloadExpiresAt:    payloadExpiry,
		AcceptedAt:          request.AcceptedAt,
		IdempotencyExpires:  request.IdempotencyExpires,
		DispatchRunAt:       request.DispatchRunAt,
		DispatchMaxAttempts: request.DispatchMaxAttempts,
	})
	if errors.Is(err, durable.ErrNotPending) {
		return runtime.ErrUnavailable
	}
	return err
}

// Status returns the payload-free status of an ingest.
func (b *Backend) Status(ctx context.Context, assignmentID, ingestID string) (runtime.IngestStatus, error) {
	row, err := b.repo.LoadIngestStatus(ctx, assignmentID, ingestID)
	if errors.Is(err, durable.ErrNotFound) {
		return runtime.IngestStatus{}, runtime.ErrNotFound
	}
	if err != nil {
		return runtime.IngestStatus{}, err
	}
	return runtime.IngestStatus{
		IngestID:     ingestID,
		AssignmentID: assignmentID,
		Headers: runtime.StableHeaders{
			EventID:     row.EventID,
			EventType:   row.EventType,
			Route:       row.Route,
			ContentType: row.ContentType,
		},
		BodySHA256:           runtime.Hash(row.BodySHA256),
		AcceptedAt:           row.AcceptedAt,
		IdempotencyExpiresAt: row.IdempotencyExpiresAt,
		UpdatedAt:            row.UpdatedAt,
		IngestState:          "durable",
		IdempotencyState:     "committed",
		DispatchState:        mapDispatchState(row.DispatchState),
	}, nil
}

func mapDispatchState(state string) string {
	switch state {
	case "ready", "retry":
		return "queued"
	case "leased":
		return "claimed"
	case "succeeded":
		return "completed"
	case "dead":
		return "failed"
	default:
		return "queued"
	}
}

// ClaimJob claims a durable dispatch job under a fencing lease.
func (b *Backend) ClaimJob(ctx context.Context, assignmentID, kind, owner, token string, lease time.Duration) (runtime.Job, bool, error) {
	job, ok, err := b.repo.ClaimJob(ctx, assignmentID, kind, owner, token, lease)
	if err != nil || !ok {
		return runtime.Job{}, ok, err
	}
	return toRuntimeJob(job), true, nil
}

// LoadDelivery loads the committed delivery context for a claimed job.
func (b *Backend) LoadDelivery(ctx context.Context, job runtime.Job) (runtime.DeliveryContext, error) {
	endpoint, ok := b.endpoints[job.EndpointSnapshotID]
	if !ok {
		return runtime.DeliveryContext{}, runtime.ErrNotFound
	}
	payload, err := b.repo.LoadPayloadCiphertext(ctx, job.AssignmentID, job.PayloadID)
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
	plaintext, err := b.sealer.Open(runtime.SealedPayload{
		Ciphertext:       payload.Ciphertext,
		Nonce:            payload.Nonce,
		KeyReference:     payload.KeyReference,
		KeyVersion:       payload.KeyVersion,
		CiphertextSHA256: runtime.Hash(payload.CiphertextSHA256),
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

// CompleteJob settles a successful job.
func (b *Backend) CompleteJob(ctx context.Context, lease runtime.JobLease) error {
	err := b.repo.CompleteJob(ctx, durable.JobLease(lease))
	return mapLeaseError(err)
}

// RetryJob reschedules or dead-letters a job.
func (b *Backend) RetryJob(ctx context.Context, lease runtime.JobLease, delay time.Duration, errorCode string) error {
	err := b.repo.RetryJob(ctx, durable.JobLease(lease), delay, errorCode)
	return mapLeaseError(err)
}

// FailJob terminally dead-letters a job under lease.
func (b *Backend) FailJob(ctx context.Context, lease runtime.JobLease, errorCode string) error {
	return mapLeaseError(b.repo.FailJob(ctx, durable.JobLease(lease), errorCode))
}

// RecordAttempt writes the immutable attempt and its metadata outbox row.
func (b *Backend) RecordAttempt(ctx context.Context, attempt runtime.AttemptRecord, observation runtime.OutboxObservation) error {
	return b.repo.RecordAttemptAndOutbox(ctx, toDurableAttempt(attempt), toDurableOutbox(observation))
}

// ClaimOutbox claims a metadata outbox row under a fencing lease.
func (b *Backend) ClaimOutbox(ctx context.Context, assignmentID, owner, token string, lease time.Duration) (runtime.OutboxItem, bool, error) {
	item, ok, err := b.repo.ClaimOutbox(ctx, assignmentID, owner, token, lease)
	if err != nil || !ok {
		return runtime.OutboxItem{}, ok, err
	}
	return toRuntimeOutboxItem(item), true, nil
}

// PublishOutbox marks a claimed outbox row published.
func (b *Backend) PublishOutbox(ctx context.Context, lease runtime.OutboxLease) error {
	return mapLeaseError(b.repo.PublishOutbox(ctx, durable.OutboxLease(lease)))
}

// RetryOutbox reschedules a claimed outbox row.
func (b *Backend) RetryOutbox(ctx context.Context, lease runtime.OutboxLease, delay time.Duration, errorCode string) error {
	return mapLeaseError(b.repo.RetryOutbox(ctx, durable.OutboxLease(lease), delay, errorCode))
}

// ListDeadJobs returns dead dispatch jobs for an assignment.
func (b *Backend) ListDeadJobs(ctx context.Context, assignmentID string, limit int) ([]runtime.Job, error) {
	jobs, err := b.repo.ListDeadJobs(ctx, assignmentID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toRuntimeJob(job))
	}
	return out, nil
}

// RedriveJob returns a dead job to ready.
func (b *Backend) RedriveJob(ctx context.Context, assignmentID, jobID string, runAt time.Time) error {
	err := b.repo.RedriveJob(ctx, assignmentID, jobID, runAt)
	if errors.Is(err, durable.ErrNotFound) {
		return runtime.ErrNotFound
	}
	return err
}

// ReadyDispatch lists claimable dispatch jobs for the dispatch relay to publish
// advisory signals. It reads committed state only and never mutates the queue.
func (b *Backend) ReadyDispatch(ctx context.Context, limit int) ([]runtime.Job, error) {
	jobs, err := b.repo.ListReadyDispatch(ctx, limit)
	if err != nil {
		return nil, err
	}
	out := make([]runtime.Job, 0, len(jobs))
	for _, job := range jobs {
		out = append(out, toRuntimeJob(job))
	}
	return out, nil
}

func mapLeaseError(err error) error {
	if errors.Is(err, durable.ErrLeaseLost) {
		return runtime.ErrLeaseLost
	}
	return err
}

// Compile-time assertion that the PostgreSQL backend satisfies the full runtime
// contract.
var _ runtime.Backend = (*Backend)(nil)
