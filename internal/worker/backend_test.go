package worker_test

import (
	"context"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/transport"
	"github.com/hookship/hook-service/internal/worker"
)

// minimalWorkerBackend implements exactly worker.Backend and nothing else. It
// demonstrates that the segregated consumer interface lets a test double be a
// small stub instead of a full runtime.Backend (which carries the credential,
// sealer, ingest-store, and diagnostics ports the delivery loop never touches).
type minimalWorkerBackend struct{}

func (minimalWorkerBackend) Assignments(context.Context) ([]runtime.Assignment, error) {
	return nil, nil
}

func (minimalWorkerBackend) ClaimJob(context.Context, string, string, string, string, time.Duration) (runtime.Job, bool, error) {
	return runtime.Job{}, false, nil
}

func (minimalWorkerBackend) LoadDelivery(context.Context, runtime.Job) (runtime.DeliveryContext, error) {
	return runtime.DeliveryContext{}, nil
}

func (minimalWorkerBackend) CompleteJob(context.Context, runtime.JobLease) error { return nil }

func (minimalWorkerBackend) RetryJob(context.Context, runtime.JobLease, time.Duration, string) error {
	return nil
}

func (minimalWorkerBackend) FailJob(context.Context, runtime.JobLease, string) error { return nil }

func (minimalWorkerBackend) RecordAttempt(context.Context, runtime.AttemptRecord, runtime.OutboxObservation) error {
	return nil
}

func (minimalWorkerBackend) ClaimOutbox(context.Context, string, string, string, time.Duration) (runtime.OutboxItem, bool, error) {
	return runtime.OutboxItem{}, false, nil
}

func (minimalWorkerBackend) PublishOutbox(context.Context, runtime.OutboxLease) error { return nil }

func (minimalWorkerBackend) RetryOutbox(context.Context, runtime.OutboxLease, time.Duration, string) error {
	return nil
}

// Compile-time proof that the minimal stub satisfies the consumer interface.
var _ worker.Backend = minimalWorkerBackend{}

// TestWorkerAcceptsMinimalBackend proves the worker can be constructed from a
// double that implements only the segregated queue/outbox/assignment ports.
func TestWorkerAcceptsMinimalBackend(t *testing.T) {
	client := transport.NewClient(transport.Options{AllowLoopback: true, AllowPlaintext: true, TotalTimeout: time.Second})
	w, err := worker.New(worker.Options{Backend: minimalWorkerBackend{}, Transport: client})
	if err != nil {
		t.Fatalf("New with minimal backend: %v", err)
	}
	if w == nil {
		t.Fatal("expected a worker")
	}
}
