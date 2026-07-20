package outboxsink_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/outboxsink"
)

func sampleObservation() outboxsink.Observation {
	status := 200
	return outboxsink.Observation{
		SchemaVersion:      "1",
		AssignmentID:       "asg_orders",
		DeliveryID:         "del_1",
		AttemptID:          "att_1",
		EndpointSnapshotID: "snap_1",
		EventKind:          "delivery.attempted",
		Outcome:            "success",
		ResponseStatus:     &status,
		OccurredAt:         time.Unix(1_700_000_000, 0).UTC(),
	}
}

func TestLocalLogSinkDoesNotPanic(t *testing.T) {
	sink := outboxsink.NewLocalLogSink(nil)
	if err := sink.Publish(context.Background(), sampleObservation()); err != nil {
		t.Fatal(err)
	}
	if sink.Name() != "local" {
		t.Fatalf("name = %q", sink.Name())
	}
	_ = sink.Close()
}

func TestLocalFileSinkWritesJSONLines(t *testing.T) {
	path := filepath.Join(t.TempDir(), "observations.jsonl")
	sink, err := outboxsink.NewLocalFileSink(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := sink.Publish(context.Background(), sampleObservation()); err != nil {
		t.Fatal(err)
	}
	_ = sink.Close()

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	line := strings.TrimSpace(string(data))
	var decoded outboxsink.Observation
	if err := json.Unmarshal([]byte(line), &decoded); err != nil {
		t.Fatalf("line is not valid JSON: %v", err)
	}
	if decoded.DeliveryID != "del_1" {
		t.Fatalf("delivery id = %q", decoded.DeliveryID)
	}
	// No payload/secret content may appear.
	if strings.Contains(line, "ciphertext") || strings.Contains(line, "secret") {
		t.Fatal("observation leaked forbidden content")
	}
}

func TestPlatformSinkPostsAuthenticatedObservation(t *testing.T) {
	var mu sync.Mutex
	var received outboxsink.Observation
	var auth, schema, contentType string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = r.Header.Get("Authorization")
		schema = r.Header.Get("Hookship-Metadata-Schema")
		contentType = r.Header.Get("Content-Type")
		_ = json.NewDecoder(r.Body).Decode(&received)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()

	sink, err := outboxsink.NewPlatformSink(outboxsink.PlatformOptions{URL: server.URL, Token: "secret-token"})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err := sink.Publish(context.Background(), sampleObservation()); err != nil {
		t.Fatal(err)
	}

	mu.Lock()
	defer mu.Unlock()
	if auth != "Bearer secret-token" {
		t.Fatalf("authorization = %q", auth)
	}
	if schema != "1" || contentType != "application/json" {
		t.Fatalf("headers schema=%q content-type=%q", schema, contentType)
	}
	if received.DeliveryID != "del_1" || received.Outcome != "success" {
		t.Fatalf("observation mismatch: %+v", received)
	}
}

func TestPlatformSinkReschedulesOnNon2xx(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer server.Close()
	sink, err := outboxsink.NewPlatformSink(outboxsink.PlatformOptions{URL: server.URL, Token: "t"})
	if err != nil {
		t.Fatal(err)
	}
	defer sink.Close()
	if err := sink.Publish(context.Background(), sampleObservation()); err == nil {
		t.Fatal("expected error on non-2xx so the outbox row is rescheduled")
	}
}

func TestPlatformSinkValidatesOptions(t *testing.T) {
	if _, err := outboxsink.NewPlatformSink(outboxsink.PlatformOptions{Token: "t"}); err == nil {
		t.Fatal("expected missing URL rejection")
	}
	if _, err := outboxsink.NewPlatformSink(outboxsink.PlatformOptions{URL: "https://x.test"}); err == nil {
		t.Fatal("expected missing token rejection")
	}
}

func TestMetadataWireFixtureMatchesObservationShape(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("testdata", "metadata-observation.json"))
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Example json.RawMessage `json:"example"`
	}
	if err := json.Unmarshal(data, &fixture); err != nil {
		t.Fatal(err)
	}
	var observation outboxsink.Observation
	if err := json.Unmarshal(fixture.Example, &observation); err != nil {
		t.Fatalf("fixture example does not match Observation: %v", err)
	}
	if observation.EventKind != "delivery.attempted" {
		t.Fatalf("fixture event kind = %q", observation.EventKind)
	}
}
