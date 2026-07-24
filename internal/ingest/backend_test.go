package ingest_test

import (
	"context"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/ingest"
	"github.com/hookship/hook-service/internal/runtime"
)

// minimalIngestBackend implements exactly ingest.Backend and nothing else. It
// demonstrates that the segregated consumer interface lets a test double be a
// small, focused stub instead of a full runtime.Backend (which carries the
// queue, outbox, and diagnostics ports the ingest path never touches).
type minimalIngestBackend struct{}

func (minimalIngestBackend) ResolveCredential(context.Context, string) (runtime.Assignment, error) {
	return runtime.Assignment{}, runtime.ErrNotFound
}

func (minimalIngestBackend) ReserveNonce(context.Context, string, string, time.Time, time.Time) (bool, error) {
	return true, nil
}

func (minimalIngestBackend) Seal([]byte) (runtime.SealedPayload, error) {
	return runtime.SealedPayload{}, nil
}

func (minimalIngestBackend) ReserveIdempotency(context.Context, runtime.ReservationRequest) (runtime.ReservationResult, error) {
	return runtime.ReservationResult{}, nil
}

func (minimalIngestBackend) Accept(context.Context, runtime.AcceptanceRequest) error { return nil }

func (minimalIngestBackend) Status(context.Context, string, string) (runtime.IngestStatus, error) {
	return runtime.IngestStatus{}, runtime.ErrNotFound
}

func (minimalIngestBackend) Durable() bool { return true }

// Compile-time proof that the minimal stub satisfies the consumer interface.
var _ ingest.Backend = minimalIngestBackend{}

// TestHandlerAcceptsMinimalBackend proves the handler can be constructed from a
// double that implements only the seven segregated ingest ports.
func TestHandlerAcceptsMinimalBackend(t *testing.T) {
	handler, err := ingest.New(ingest.Options{Backend: minimalIngestBackend{}})
	if err != nil {
		t.Fatalf("New with minimal backend: %v", err)
	}
	if handler == nil {
		t.Fatal("expected a handler")
	}
}
