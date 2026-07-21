//go:build integration

package postgres_test

import (
	"context"
	"errors"
	"os"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/backend/backendtest"
	"github.com/hookship/hook-service/internal/backend/objectstore"
	backendpg "github.com/hookship/hook-service/internal/backend/postgres"
	"github.com/hookship/hook-service/internal/runtime"
)

// objectContractStore returns a PayloadStore for the object-store contract. When
// MinIO integration environment is configured it uses the real S3 client so the
// full acceptance path is exercised end to end; otherwise it uses the in-memory
// reference store. The returned counter reports how many objects were written.
func objectContractStore(t *testing.T) runtime.PayloadStore {
	t.Helper()
	endpoint := os.Getenv("MINIO_TEST_ENDPOINT")
	bucket := os.Getenv("MINIO_TEST_BUCKET")
	access := os.Getenv("MINIO_TEST_ACCESS_KEY")
	secret := os.Getenv("MINIO_TEST_SECRET_KEY")
	if endpoint == "" || bucket == "" || access == "" || secret == "" {
		return objectstore.NewMemoryStore()
	}
	region := os.Getenv("MINIO_TEST_REGION")
	if region == "" {
		region = "us-east-1"
	}
	client, err := objectstore.New(objectstore.Config{
		Endpoint:    endpoint,
		Region:      region,
		Bucket:      bucket,
		PathStyle:   true,
		UseTLS:      os.Getenv("MINIO_TEST_USE_TLS") == "true",
		Credentials: awsv4.Credentials{AccessKeyID: access, SecretAccessKey: secret},
		Timeout:     15 * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return client
}

// TestObjectStorePayloadBackendContract runs the full shared backend contract
// against the PostgreSQL profile with the in-memory object-store payload
// overlay, so the object acceptance path (stage -> claim -> immutable PUT ->
// finalize -> commit) and object-backed LoadDelivery are held to identical
// functional behavior as the PostgreSQL-byte default.
func TestObjectStorePayloadBackendContract(t *testing.T) {
	backendtest.RunContract(t, func(t *testing.T) (runtime.Backend, runtime.Assignment, func()) {
		pool, cleanup := newIsolatedPool(t)
		sealer := newTestSealer(t)
		store := objectContractStore(t)
		base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
		backend, err := backendpg.New(context.Background(), pool, []runtime.Assignment{base}, sealer, runtime.SystemClock{}, false,
			backendpg.WithPayloadStore(store))
		if err != nil {
			t.Fatal(err)
		}
		resolved, err := backend.ResolveCredential(context.Background(), base.CredentialID)
		if err != nil {
			t.Fatal(err)
		}
		return backend, resolved, func() {
			backend.Close()
			cleanup()
		}
	})
}

// TestObjectStoreAcceptanceIsRestartSafe proves LoadDelivery reads committed
// ciphertext from the object store on a fresh backend (a different replica or a
// restarted process), never depending on the process that accepted the event.
func TestObjectStoreAcceptanceIsRestartSafe(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	sealer := newTestSealer(t)
	store := objectstore.NewMemoryStore()
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")

	backend, err := backendpg.New(ctx, pool, []runtime.Assignment{base}, sealer, runtime.SystemClock{}, false,
		backendpg.WithPayloadStore(store))
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := backend.ResolveCredential(ctx, base.CredentialID)

	body := []byte(`{"object":true}`)
	acceptObject(t, ctx, backend, resolved, body, "op-object-restart")

	if store.ObjectCount() == 0 {
		t.Fatal("expected ciphertext objects written to the store")
	}
	backend.Close()

	// A brand-new backend over the same database and the same object store still
	// serves the delivery from committed durable state.
	reopened, err := backendpg.New(ctx, pool, []runtime.Assignment{base}, sealer, runtime.SystemClock{}, false,
		backendpg.WithPayloadStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()

	job, ok, err := reopened.ClaimJob(ctx, resolved.ID, runtime.JobKindDispatch, "worker", "token", 30*time.Second)
	if err != nil || !ok {
		t.Fatalf("claim after reopen: ok=%v err=%v", ok, err)
	}
	delivery, err := reopened.LoadDelivery(ctx, job)
	if err != nil {
		t.Fatalf("load object delivery after reopen: %v", err)
	}
	if string(delivery.Body) != string(body) {
		t.Fatalf("recovered payload mismatch: %q", delivery.Body)
	}
}

// TestObjectStorePutFailureYieldsNoFalseAcceptance verifies that a failed object
// PUT returns retryable unavailability and never a false 202: the ingest is not
// committed and no committed ciphertext exists.
func TestObjectStorePutFailureYieldsNoFalseAcceptance(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	sealer := newTestSealer(t)
	store := objectstore.NewMemoryStore()
	store.SetPutFault(func(string) error { return errors.New("object store unavailable") })
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")

	backend, err := backendpg.New(ctx, pool, []runtime.Assignment{base}, sealer, runtime.SystemClock{}, false,
		backendpg.WithPayloadStore(store))
	if err != nil {
		t.Fatal(err)
	}
	defer backend.Close()
	resolved, _ := backend.ResolveCredential(ctx, base.CredentialID)

	body := []byte(`{"object":"fails"}`)
	hash := runtime.SHA256(body)
	headers := runtime.StableHeaders{EventID: "evt_f", EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_objfail", AssignmentID: resolved.ID, IdempotencyKey: "op-objfail",
		BodySHA256: hash, Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := backend.Seal(body)
	now := runtime.SystemClock{}.Now()
	err = backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: resolved, LedgerID: reservation.Reservation.LedgerID, IdempotencyKey: "op-objfail",
		IngestID: "ing_objfail", PayloadID: "pay_objfail", DeliveryID: "del_objfail", DispatchJobID: "job_objfail",
		EndpointSnapshotID: resolved.EndpointSnapshotID, Payload: sealed, BodySHA256: hash, Headers: headers,
		AcceptedAt: now, IdempotencyExpires: now.Add(25 * time.Hour), PayloadExpiresAt: now.Add(48 * time.Hour),
		DispatchRunAt: now, DispatchMaxAttempts: 5,
	})
	if !errors.Is(err, runtime.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable on object PUT failure, got %v", err)
	}
	if _, err := backend.Status(ctx, resolved.ID, "ing_objfail"); !errors.Is(err, runtime.ErrNotFound) {
		t.Fatalf("failed acceptance must not be observable as accepted: %v", err)
	}
	if store.Has("hookship") {
		t.Fatal("no committed ciphertext should exist after a failed PUT")
	}
}

// acceptObject reserves and accepts a body through the object-store overlay and
// returns after the acceptance barrier is crossed.
func acceptObject(t *testing.T, ctx context.Context, backend runtime.Backend, assignment runtime.Assignment, body []byte, key string) {
	t.Helper()
	hash := runtime.SHA256(body)
	headers := runtime.StableHeaders{EventID: "evt_" + key, EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_" + key, AssignmentID: assignment.ID, IdempotencyKey: key,
		BodySHA256: hash, Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := backend.Seal(body)
	now := runtime.SystemClock{}.Now()
	if err := backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: assignment, LedgerID: reservation.Reservation.LedgerID, IdempotencyKey: key,
		IngestID: "ing_" + key, PayloadID: "pay_" + key, DeliveryID: "del_" + key, DispatchJobID: "job_" + key,
		EndpointSnapshotID: assignment.EndpointSnapshotID, Payload: sealed, BodySHA256: hash, Headers: headers,
		AcceptedAt: now, IdempotencyExpires: now.Add(25 * time.Hour), PayloadExpiresAt: now.Add(48 * time.Hour),
		DispatchRunAt: now, DispatchMaxAttempts: 5,
	}); err != nil {
		t.Fatalf("accept object: %v", err)
	}
}
