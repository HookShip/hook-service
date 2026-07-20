package relay_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/backend/relay"
)

func TestSignalCodecRoundTrip(t *testing.T) {
	signal := relay.NewSignal("asg_1", "job_1", "snap_1", time.Now().UnixMilli())
	encoded, err := relay.Encode(signal)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := relay.Decode(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if decoded != signal {
		t.Fatalf("round trip mismatch: %+v vs %+v", decoded, signal)
	}
}

func TestDecodeRejectsUnknownSchema(t *testing.T) {
	if _, err := relay.Decode([]byte(`{"schemaVersion":"dispatch_signal/v2","assignmentId":"a","jobId":"j"}`)); err == nil {
		t.Fatal("expected unknown schema rejection")
	}
	if _, err := relay.Decode([]byte(`{"schemaVersion":"dispatch_signal/v1"}`)); err == nil {
		t.Fatal("expected missing identifier rejection")
	}
}

func TestPartitionKeyPrefersEndpoint(t *testing.T) {
	withEndpoint := relay.Signal{AssignmentID: "a", EndpointSnapshotID: "snap"}
	if withEndpoint.PartitionKey() != "snap" {
		t.Fatalf("partition key = %q", withEndpoint.PartitionKey())
	}
	withoutEndpoint := relay.Signal{AssignmentID: "a"}
	if withoutEndpoint.PartitionKey() != "a" {
		t.Fatalf("partition key = %q", withoutEndpoint.PartitionKey())
	}
}

type fakePublisher struct {
	mu      sync.Mutex
	signals []relay.Signal
	closed  bool
}

func (f *fakePublisher) Publish(_ context.Context, signal relay.Signal) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.signals = append(f.signals, signal)
	return nil
}

func (f *fakePublisher) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.closed = true
	return nil
}

func (f *fakePublisher) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.signals)
}

func TestRelayerPublishesAndDeduplicates(t *testing.T) {
	publisher := &fakePublisher{}
	source := func(_ context.Context, _ int) ([]relay.Signal, error) {
		return []relay.Signal{
			relay.NewSignal("asg_1", "job_1", "snap_1", 0),
			relay.NewSignal("asg_1", "job_2", "snap_1", 0),
		}, nil
	}
	relayer := relay.NewRelayer(relay.RelayerOptions{
		Source:    source,
		Publisher: publisher,
		Interval:  5 * time.Millisecond,
		DedupTTL:  time.Hour,
	})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = relayer.Run(ctx); close(done) }()

	deadline := time.Now().Add(2 * time.Second)
	for publisher.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	// Let several more intervals pass; dedup must prevent republishing.
	time.Sleep(50 * time.Millisecond)
	cancel()
	<-done

	if got := publisher.count(); got != 2 {
		t.Fatalf("expected exactly two published signals (deduped), got %d", got)
	}
}

type fakeConsumer struct {
	signals []relay.Signal
}

func (f *fakeConsumer) Consume(ctx context.Context, handle func(context.Context, relay.Signal) error) error {
	for _, signal := range f.signals {
		if err := handle(ctx, signal); err != nil {
			return err
		}
	}
	<-ctx.Done()
	return nil
}

func (f *fakeConsumer) Close() error { return nil }

func TestNotifierWakesOnSignal(t *testing.T) {
	wake := make(chan struct{}, 2)
	consumer := &fakeConsumer{signals: []relay.Signal{
		relay.NewSignal("a", "j1", "s", 0),
		relay.NewSignal("a", "j2", "s", 0),
	}}
	notifier := relay.NewNotifier(relay.NotifierOptions{Consumer: consumer, Wake: wake})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = notifier.Run(ctx) }()

	for i := 0; i < 2; i++ {
		select {
		case <-wake:
		case <-time.After(time.Second):
			t.Fatal("expected wake signal")
		}
	}
}
