//go:build integration

package postgres_test

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"os"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend/backendtest"
	backendpg "github.com/hookship/hook-service/internal/backend/postgres"
	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/payloadcrypto"
	"github.com/hookship/hook-service/internal/runtime"
)

func TestPostgresBackendContract(t *testing.T) {
	backendtest.RunContract(t, func(t *testing.T) (runtime.Backend, runtime.Assignment, func()) {
		pool, cleanup := newIsolatedPool(t)
		sealer := newTestSealer(t)
		base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")
		backend, err := backendpg.New(context.Background(), backendpg.Params{Pool: pool, Assignments: []runtime.Assignment{base}, Sealer: sealer, Clock: runtime.SystemClock{}})
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

func TestPostgresAcceptanceIsDurableAcrossReopen(t *testing.T) {
	pool, cleanup := newIsolatedPool(t)
	defer cleanup()
	ctx := context.Background()
	sealer := newTestSealer(t)
	base := assignmentconfig.DefaultEvaluationAssignment("https://sink.example/hooks")

	backend, err := backendpg.New(ctx, backendpg.Params{Pool: pool, Assignments: []runtime.Assignment{base}, Sealer: sealer, Clock: runtime.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	resolved, _ := backend.ResolveCredential(ctx, base.CredentialID)

	body := []byte(`{"durable":true}`)
	hash := runtime.SHA256(body)
	headers := runtime.StableHeaders{EventID: "evt_d", EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_durable", AssignmentID: resolved.ID, IdempotencyKey: "op-durable",
		BodySHA256: hash, Headers: headers,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, _ := backend.Seal(body)
	now := runtime.SystemClock{}.Now()
	if err := backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: resolved, LedgerID: reservation.Reservation.LedgerID, IdempotencyKey: "op-durable",
		IngestID: "ing_durable", PayloadID: "pay_durable", DeliveryID: "del_durable", DispatchJobID: "job_durable",
		EndpointSnapshotID: resolved.EndpointSnapshotID, Payload: sealed, BodySHA256: hash, Headers: headers,
		AcceptedAt: now, IdempotencyExpires: now.Add(25 * 3600 * 1e9), PayloadExpiresAt: now.Add(48 * 3600 * 1e9),
		DispatchRunAt: now, DispatchMaxAttempts: 5,
	}); err != nil {
		t.Fatal(err)
	}
	// A brand-new backend over the same database (simulating a restart) still
	// sees the accepted ingest and can deliver from the committed payload.
	backend.Close()
	reopened, err := backendpg.New(ctx, backendpg.Params{Pool: pool, Assignments: []runtime.Assignment{base}, Sealer: sealer, Clock: runtime.SystemClock{}})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	status, err := reopened.Status(ctx, resolved.ID, "ing_durable")
	if err != nil {
		t.Fatalf("accepted ingest must survive a reopen: %v", err)
	}
	if status.DispatchState != "queued" {
		t.Fatalf("dispatch state = %q", status.DispatchState)
	}
	job, ok, err := reopened.ClaimJob(ctx, resolved.ID, runtime.JobKindDispatch, "worker", "token", 30_000_000_000)
	if err != nil || !ok {
		t.Fatalf("claim after reopen: ok=%v err=%v", ok, err)
	}
	delivery, err := reopened.LoadDelivery(ctx, job)
	if err != nil {
		t.Fatal(err)
	}
	if string(delivery.Body) != string(body) {
		t.Fatalf("recovered payload mismatch: %q", delivery.Body)
	}
}

func newTestSealer(t *testing.T) runtime.PayloadSealer {
	t.Helper()
	key := make([]byte, payloadcrypto.KeySize)
	for i := range key {
		key[i] = byte(i * 3)
	}
	sealer, err := payloadcrypto.NewSealer(key, "postgres://test/key/1", "1")
	if err != nil {
		t.Fatal(err)
	}
	return sealer
}

func newIsolatedPool(t *testing.T) (*pgxpool.Pool, func()) {
	t.Helper()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("TEST_DATABASE_URL is not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	suffix := make([]byte, 8)
	if _, err := rand.Read(suffix); err != nil {
		t.Fatal(err)
	}
	schema := "hookship_backend_" + hex.EncodeToString(suffix)
	identifier := pgx.Identifier{schema}.Sanitize()
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+identifier); err != nil {
		admin.Close()
		t.Fatal(err)
	}

	config, err := pgxpool.ParseConfig(databaseURL)
	if err != nil {
		admin.Close()
		t.Fatal(err)
	}
	config.ConnConfig.RuntimeParams["search_path"] = identifier
	config.MaxConns = 8
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	if err := durable.Migrate(ctx, pool); err != nil {
		pool.Close()
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
		t.Fatal(err)
	}
	return pool, func() {
		pool.Close()
		_, _ = admin.Exec(context.Background(), "DROP SCHEMA "+identifier+" CASCADE")
		admin.Close()
	}
}
