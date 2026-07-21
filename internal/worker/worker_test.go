package worker_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/assignmentconfig"
	"github.com/hookship/hook-service/internal/backend/memory"
	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/signing"
	"github.com/hookship/hook-service/internal/transport"
	"github.com/hookship/hook-service/internal/worker"
)

func acceptJob(t *testing.T, backend runtime.Backend, assignment runtime.Assignment, body []byte, key string) {
	t.Helper()
	ctx := context.Background()
	now := time.Now().UTC().Truncate(time.Microsecond)
	hash := runtime.SHA256(body)
	headers := runtime.StableHeaders{EventID: "evt_" + key, EventType: "order.created", Route: "orders.primary", ContentType: "application/json"}
	reservation, err := backend.ReserveIdempotency(ctx, runtime.ReservationRequest{
		LedgerID: "led_" + key, AssignmentID: assignment.ID, IdempotencyKey: key, BodySHA256: hash, Headers: headers, ReservedAt: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	sealed, err := backend.Seal(body)
	if err != nil {
		t.Fatal(err)
	}
	if err := backend.Accept(ctx, runtime.AcceptanceRequest{
		Assignment: assignment, LedgerID: reservation.Reservation.LedgerID, IdempotencyKey: key,
		IngestID: "ing_" + key, PayloadID: "pay_" + key, DeliveryID: "del_" + key, DispatchJobID: "job_" + key,
		EndpointSnapshotID: assignment.EndpointSnapshotID, Payload: sealed, BodySHA256: hash, Headers: headers,
		AcceptedAt: now, IdempotencyExpires: now.Add(24 * time.Hour), PayloadExpiresAt: now.Add(48 * time.Hour),
		DispatchRunAt: now, DispatchMaxAttempts: 2,
	}); err != nil {
		t.Fatal(err)
	}
}

func newBackend(t *testing.T, sinkURL string) (runtime.Backend, runtime.Assignment) {
	t.Helper()
	base := assignmentconfig.DefaultEvaluationAssignment(sinkURL)
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

func newWorker(t *testing.T, backend runtime.Backend) *worker.Worker {
	t.Helper()
	client := transport.NewClient(transport.Options{AllowLoopback: true, AllowPlaintext: true, TotalTimeout: 3 * time.Second})
	w, err := worker.New(worker.Options{
		Backend:         backend,
		Transport:       client,
		Concurrency:     2,
		LeaseDuration:   5 * time.Second,
		DeliveryTimeout: 2 * time.Second,
		BaseBackoff:     time.Millisecond,
		MaxBackoff:      10 * time.Millisecond,
		PollInterval:    5 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	return w
}

func TestWorkerDeliversSignedPayload(t *testing.T) {
	var mu sync.Mutex
	var receivedBody []byte
	var signatureOK bool
	var receivedID string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = readFull(r, body)
		id := r.Header.Get("webhook-id")
		ts := r.Header.Get("webhook-timestamp")
		sig := r.Header.Get("webhook-signature")

		signer, _ := signing.NewSigner([]byte(evaluationSigningSecret()))
		verifyErr := signer.Verify(id, ts, sig, body, time.Hour)

		mu.Lock()
		receivedBody = body
		receivedID = id
		signatureOK = verifyErr == nil
		mu.Unlock()
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	backend, assignment := newBackend(t, server.URL)
	acceptJob(t, backend, assignment, []byte(`{"order_id":"777"}`), "op-worker-success")

	ctx, cancel := context.WithCancel(context.Background())
	w := newWorker(t, backend)
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	waitFor(t, 3*time.Second, func() bool {
		status, err := backend.Status(context.Background(), assignment.ID, "ing_op-worker-success")
		return err == nil && status.DispatchState == "completed"
	})
	cancel()
	<-done

	mu.Lock()
	defer mu.Unlock()
	if !signatureOK {
		t.Fatal("delivered Standard Webhooks signature did not verify")
	}
	if string(receivedBody) != `{"order_id":"777"}` {
		t.Fatalf("payload not delivered exactly: %q", receivedBody)
	}
	if receivedID != "del_op-worker-success" {
		t.Fatalf("webhook-id = %q", receivedID)
	}
}

func TestWorkerRetriesThenDeadLetters(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	backend, assignment := newBackend(t, server.URL)
	acceptJob(t, backend, assignment, []byte(`{"order_id":"fail"}`), "op-worker-dlq")

	ctx, cancel := context.WithCancel(context.Background())
	w := newWorker(t, backend)
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	waitFor(t, 5*time.Second, func() bool {
		dead, err := backend.ListDeadJobs(context.Background(), assignment.ID, 10)
		return err == nil && len(dead) == 1
	})
	cancel()
	<-done

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got < 2 {
		t.Fatalf("expected at least two delivery attempts before DLQ, got %d", got)
	}
	status, err := backend.Status(context.Background(), assignment.ID, "ing_op-worker-dlq")
	if err != nil {
		t.Fatal(err)
	}
	if status.DispatchState != "failed" {
		t.Fatalf("dispatch state = %q, want failed", status.DispatchState)
	}
}

func TestWorkerTerminalOn4xxDoesNotRetry(t *testing.T) {
	var attempts int
	var mu sync.Mutex
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		attempts++
		mu.Unlock()
		w.WriteHeader(http.StatusBadRequest)
	}))
	defer server.Close()

	backend, assignment := newBackend(t, server.URL)
	acceptJob(t, backend, assignment, []byte(`{"order_id":"bad"}`), "op-worker-terminal")

	ctx, cancel := context.WithCancel(context.Background())
	w := newWorker(t, backend)
	done := make(chan struct{})
	go func() { _ = w.Run(ctx); close(done) }()

	waitFor(t, 5*time.Second, func() bool {
		dead, err := backend.ListDeadJobs(context.Background(), assignment.ID, 10)
		return err == nil && len(dead) == 1
	})
	cancel()
	<-done

	mu.Lock()
	got := attempts
	mu.Unlock()
	if got != 1 {
		t.Fatalf("terminal 4xx should not be retried; got %d attempts", got)
	}
}

func evaluationSigningSecret() string {
	// Mirror the fixed evaluation signing secret used by the default assignment.
	return "whsec_ZXZhbHVhdGlvbi1zaWduaW5nLXNlY3JldC0wMDAxAAA="
}

func readFull(r *http.Request, buffer []byte) (int, error) {
	total := 0
	for total < len(buffer) {
		n, err := r.Body.Read(buffer[total:])
		total += n
		if err != nil {
			return total, err
		}
	}
	return total, nil
}

func waitFor(t *testing.T, timeout time.Duration, condition func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if condition() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("condition not met within %s", timeout)
}
