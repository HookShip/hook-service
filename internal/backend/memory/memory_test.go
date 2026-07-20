package memory_test

import (
	"context"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend/backendtest"
	"github.com/hookship/hook-service/internal/backend/memory"
	"github.com/hookship/hook-service/internal/runtime"
)

func newMemoryBackend(t *testing.T) (runtime.Backend, runtime.Assignment) {
	t.Helper()
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
	backend, err := memory.New([]runtime.Assignment{base}, runtime.SystemClock{}, nil, memory.DefaultLimits())
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := backend.ResolveCredential(context.Background(), base.CredentialID)
	if err != nil {
		t.Fatal(err)
	}
	return backend, resolved
}

func TestMemoryBackendContract(t *testing.T) {
	backendtest.RunContract(t, func(t *testing.T) (runtime.Backend, runtime.Assignment, func()) {
		backend, assignment := newMemoryBackend(t)
		return backend, assignment, backend.Close
	})
}

func TestMemoryIsNotDurable(t *testing.T) {
	backend, _ := newMemoryBackend(t)
	if backend.Durable() {
		t.Fatal("memory backend must never report durable")
	}
	if backend.Name() != "memory" {
		t.Fatalf("name = %q", backend.Name())
	}
}

func TestMemoryResetLosesAcceptedState(t *testing.T) {
	backend, assignment := newMemoryBackend(t)
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	body := []byte(`{"order":10}`)
	bodyHash := runtime.SHA256(body)
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID:       "led_reset",
		AssignmentID:   assignment.ID,
		IdempotencyKey: "op-reset-1",
		BodySHA256:     bodyHash,
		Headers:        headers(),
		ReservedAt:     now,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := backend.Seal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment:          assignment,
		LedgerID:            reservation.Reservation.LedgerID,
		IdempotencyKey:      "op-reset-1",
		IngestID:            "ing_reset",
		PayloadID:           "pay_reset",
		DeliveryID:          "del_reset",
		DispatchJobID:       "job_reset",
		EndpointSnapshotID:  assignment.EndpointSnapshotID,
		Payload:             sealed,
		BodySHA256:          bodyHash,
		Headers:             headers(),
		AcceptedAt:          now,
		IdempotencyExpires:  now.Add(24 * time.Hour),
		PayloadExpiresAt:    now.Add(48 * time.Hour),
		DispatchRunAt:       now,
		DispatchMaxAttempts: 3,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Status(ctx, assignment.ID, "ing_reset"); err != nil {
		t.Fatalf("status before reset: %v", err)
	}

	// Reset simulates the effect of a restart: accepted data disappears.
	memoryBackend := backend.(*memory.Backend)
	if err := memoryBackend.Reset(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.Status(ctx, assignment.ID, "ing_reset"); err != runtime.ErrNotFound {
		t.Fatalf("expected accepted state to vanish after reset, got %v", err)
	}
	stats := memoryBackend.Stats()
	if stats.Ingests != 0 || stats.Jobs != 0 || stats.Ledgers != 0 {
		t.Fatalf("reset did not clear state: %+v", stats)
	}
}

func TestMemoryBoundedPayloadBackpressure(t *testing.T) {
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
	limits := memory.DefaultLimits()
	limits.MaxPayloadBytes = 8
	backend, err := memory.New([]runtime.Assignment{base}, runtime.SystemClock{}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := backend.ResolveCredential(context.Background(), base.CredentialID)
	ctx := context.Background()
	now := time.Now().UTC()
	big := []byte(`{"way too large to fit in the tiny bound"}`)
	bodyHash := runtime.SHA256(big)
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_big", AssignmentID: resolved.ID, IdempotencyKey: "op-big",
		BodySHA256: bodyHash, Headers: headers(), ReservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := backend.Seal(big)
	err = backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: resolved, LedgerID: reservation.Reservation.LedgerID, IdempotencyKey: "op-big",
		IngestID: "ing_big", PayloadID: "pay_big", DeliveryID: "del_big", DispatchJobID: "job_big",
		EndpointSnapshotID: resolved.EndpointSnapshotID, Payload: sealed, BodySHA256: bodyHash,
		Headers: headers(), AcceptedAt: now, IdempotencyExpires: now.Add(24 * time.Hour),
		DispatchRunAt: now, DispatchMaxAttempts: 3,
	})
	if err != runtime.ErrCapacity {
		t.Fatalf("expected ErrCapacity backpressure, got %v", err)
	}
}

func TestMemoryBoundedLedgerBackpressure(t *testing.T) {
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
	limits := memory.DefaultLimits()
	limits.MaxLedgers = 1
	backend, err := memory.New([]runtime.Assignment{base}, runtime.SystemClock{}, nil, limits)
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := backend.ResolveCredential(context.Background(), base.CredentialID)
	ctx := context.Background()
	now := time.Now().UTC()
	first, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_1", AssignmentID: resolved.ID, IdempotencyKey: "op-1",
		BodySHA256: runtime.SHA256([]byte("a")), Headers: headers(), ReservedAt: now,
	})
	if err != nil || first.Status != runtime.ReservationPending {
		t.Fatalf("first reserve: %v (%s)", err, first.Status)
	}
	_, err = backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_2", AssignmentID: resolved.ID, IdempotencyKey: "op-2",
		BodySHA256: runtime.SHA256([]byte("b")), Headers: headers(), ReservedAt: now,
	})
	if err != runtime.ErrCapacity {
		t.Fatalf("expected ErrCapacity at ledger bound, got %v", err)
	}
}

func headers() runtime.StableHeaders {
	return runtime.StableHeaders{
		EventID:     "evt_001",
		EventType:   "order.created",
		Route:       "orders.primary",
		ContentType: "application/json",
	}
}
