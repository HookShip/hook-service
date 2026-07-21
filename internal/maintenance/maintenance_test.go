package maintenance

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/metrics"
	"github.com/hookship/hook-service/internal/runtime"
)

type fakeRetention struct {
	mu               sync.Mutex
	assignments      []runtime.Assignment
	noncePurged      int64
	tombstones       int64
	orphaned         int64
	reclaimed        int
	outboxPurged     int64
	failReclaim      bool
	calls            map[string]int
	lastOutboxCutoff time.Time
}

func newFakeRetention() *fakeRetention {
	return &fakeRetention{
		assignments: []runtime.Assignment{{ID: "asg_1"}, {ID: "asg_2"}},
		noncePurged: 3, tombstones: 2, orphaned: 1, reclaimed: 4, outboxPurged: 5,
		calls: map[string]int{},
	}
}

func (f *fakeRetention) Assignments(context.Context) ([]runtime.Assignment, error) {
	return f.assignments, nil
}

func (f *fakeRetention) PurgeExpiredNonces(context.Context, int) (int64, error) {
	f.record("nonce")
	return f.noncePurged, nil
}

func (f *fakeRetention) AdvanceTombstones(context.Context, string, int) (int64, error) {
	f.record("tombstone")
	return f.tombstones, nil
}

func (f *fakeRetention) MarkExpiredObjectStagesOrphaned(context.Context, string, int) (int64, error) {
	f.record("orphan")
	return f.orphaned, nil
}

func (f *fakeRetention) ReclaimExpiredPayloads(context.Context, string, int) (int, error) {
	f.record("reclaim")
	if f.failReclaim {
		return 0, errors.New("reclaim boom")
	}
	return f.reclaimed, nil
}

func (f *fakeRetention) PurgeTerminalOutbox(_ context.Context, _ string, olderThan time.Time, _ int) (int64, error) {
	f.record("outbox")
	f.mu.Lock()
	f.lastOutboxCutoff = olderThan
	f.mu.Unlock()
	return f.outboxPurged, nil
}

func (f *fakeRetention) record(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls[name]++
}

func TestRunOnceReportsMetrics(t *testing.T) {
	backend := newFakeRetention()
	registry := metrics.NewRegistry()
	worker, err := New(Options{Backend: backend, Metrics: registry, OutboxRetention: time.Hour})
	if err != nil {
		t.Fatal(err)
	}
	worker.RunOnce(context.Background())

	snapshot := registry.Snapshot()
	// nonce purge runs once globally; the per-assignment operations run twice.
	if snapshot.Counters[metrics.CounterNoncesPurged] != 3 {
		t.Fatalf("nonces purged = %d", snapshot.Counters[metrics.CounterNoncesPurged])
	}
	if snapshot.Counters[metrics.CounterTombstones] != 4 { // 2 per assignment x 2
		t.Fatalf("tombstones = %d", snapshot.Counters[metrics.CounterTombstones])
	}
	if snapshot.Counters[metrics.CounterObjectsOrphaned] != 2 {
		t.Fatalf("orphaned = %d", snapshot.Counters[metrics.CounterObjectsOrphaned])
	}
	if snapshot.Counters[metrics.CounterPayloadsReclaimed] != 8 { // 4 x 2
		t.Fatalf("reclaimed = %d", snapshot.Counters[metrics.CounterPayloadsReclaimed])
	}
	if snapshot.Counters[metrics.CounterOutboxPurged] != 10 { // 5 x 2
		t.Fatalf("outbox purged = %d", snapshot.Counters[metrics.CounterOutboxPurged])
	}
	if backend.calls["nonce"] != 1 || backend.calls["reclaim"] != 2 {
		t.Fatalf("unexpected call counts: %+v", backend.calls)
	}
}

func TestRunOnceRecordsErrorsWithoutStopping(t *testing.T) {
	backend := newFakeRetention()
	backend.failReclaim = true
	registry := metrics.NewRegistry()
	worker, _ := New(Options{Backend: backend, Metrics: registry})
	worker.RunOnce(context.Background())

	snapshot := registry.Snapshot()
	if snapshot.Counters[metrics.CounterRetentionErrors] != 2 { // reclaim fails for each assignment
		t.Fatalf("retention errors = %d", snapshot.Counters[metrics.CounterRetentionErrors])
	}
	// Outbox purge still runs after the reclaim error.
	if backend.calls["outbox"] != 2 {
		t.Fatalf("outbox purge should still run after reclaim error: %+v", backend.calls)
	}
}

func TestRunStopsOnContextCancel(t *testing.T) {
	backend := newFakeRetention()
	worker, _ := New(Options{Backend: backend, Interval: time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = worker.Run(ctx)
		close(done)
	}()
	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop on cancel")
	}
}

func TestNewRequiresBackend(t *testing.T) {
	if _, err := New(Options{}); err == nil {
		t.Fatal("expected error for nil backend")
	}
}
