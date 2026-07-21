package metrics

import (
	"sync"
	"testing"
)

func TestCounterAndGauge(t *testing.T) {
	r := NewRegistry()
	r.Inc(CounterDeliveriesAttempted)
	r.Add(CounterDeliveriesAttempted, 4)
	r.AddInt(CounterDeliveriesSucceeded, 3)
	r.AddInt(CounterDeliveriesSucceeded, -2) // negative is ignored
	r.SetGauge(GaugeOpenCircuits, 2)
	if got := r.AddGauge(GaugeInFlightDeliveries, 5); got != 5 {
		t.Fatalf("gauge add = %d", got)
	}
	if got := r.AddGauge(GaugeInFlightDeliveries, -3); got != 2 {
		t.Fatalf("gauge decrement = %d", got)
	}

	snapshot := r.Snapshot()
	if snapshot.Counters[CounterDeliveriesAttempted] != 5 {
		t.Fatalf("attempted = %d", snapshot.Counters[CounterDeliveriesAttempted])
	}
	if snapshot.Counters[CounterDeliveriesSucceeded] != 3 {
		t.Fatalf("succeeded = %d", snapshot.Counters[CounterDeliveriesSucceeded])
	}
	if snapshot.Gauges[GaugeOpenCircuits] != 2 {
		t.Fatalf("open circuits = %d", snapshot.Gauges[GaugeOpenCircuits])
	}
	if snapshot.Gauges[GaugeInFlightDeliveries] != 2 {
		t.Fatalf("in flight = %d", snapshot.Gauges[GaugeInFlightDeliveries])
	}
}

func TestNilRegistryIsNoop(t *testing.T) {
	var r *Registry
	r.Inc("x")
	r.Add("x", 2)
	r.SetGauge("g", 1)
	if got := r.AddGauge("g", 1); got != 0 {
		t.Fatalf("nil gauge = %d", got)
	}
	if len(r.Snapshot().Counters) != 0 {
		t.Fatal("nil snapshot must be empty")
	}
}

func TestConcurrentCounters(t *testing.T) {
	r := NewRegistry()
	var wg sync.WaitGroup
	for i := 0; i < 50; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 100; j++ {
				r.Inc(CounterDeliveriesAttempted)
			}
		}()
	}
	wg.Wait()
	if got := r.Snapshot().Counters[CounterDeliveriesAttempted]; got != 5000 {
		t.Fatalf("concurrent counter = %d, want 5000", got)
	}
}
