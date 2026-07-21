// Package metrics is a tiny, dependency-free, payload-free metrics registry for
// operator visibility. It records named counters and gauges only — never
// request bodies, payloads, secrets, or identifiers — so it is safe to expose
// on the health surface. It is safe for concurrent use.
package metrics

import (
	"sort"
	"sync"
	"sync/atomic"
)

// Names of the counters and gauges recorded across the runtime. They are stable
// operator-facing identifiers.
const (
	CounterDeliveriesAttempted = "deliveries_attempted"
	CounterDeliveriesSucceeded = "deliveries_succeeded"
	CounterDeliveriesRetried   = "deliveries_retried"
	CounterDeliveriesDead      = "deliveries_dead"
	CounterCircuitOpenSkips    = "circuit_open_skips"
	CounterRateLimited         = "rate_limited"
	CounterConcurrencyDeferred = "concurrency_deferred"

	CounterNoncesPurged      = "retention_nonces_purged"
	CounterTombstones        = "retention_tombstones_advanced"
	CounterObjectsOrphaned   = "retention_object_stages_orphaned"
	CounterPayloadsReclaimed = "retention_payloads_reclaimed"
	CounterOutboxPurged      = "retention_outbox_purged"
	CounterRetentionErrors   = "retention_errors"

	GaugeInFlightDeliveries = "in_flight_deliveries"
	GaugeOpenCircuits       = "open_circuits"
)

// Registry holds counters and gauges.
type Registry struct {
	mu       sync.RWMutex
	counters map[string]*atomic.Uint64
	gauges   map[string]*atomic.Int64
}

// Snapshot is an immutable point-in-time view of the registry.
type Snapshot struct {
	Counters map[string]uint64 `json:"counters"`
	Gauges   map[string]int64  `json:"gauges"`
}

// NewRegistry builds an empty registry.
func NewRegistry() *Registry {
	return &Registry{
		counters: make(map[string]*atomic.Uint64),
		gauges:   make(map[string]*atomic.Int64),
	}
}

// Inc increments a counter by one. A nil registry is a no-op so call sites need
// no guards.
func (r *Registry) Inc(name string) { r.Add(name, 1) }

// Add increases a counter by delta.
func (r *Registry) Add(name string, delta uint64) {
	if r == nil {
		return
	}
	r.counter(name).Add(delta)
}

// AddInt adds a non-negative int to a counter, clamping negatives to zero.
func (r *Registry) AddInt(name string, delta int) {
	if delta <= 0 {
		return
	}
	r.Add(name, uint64(delta))
}

// SetGauge sets a gauge to an absolute value.
func (r *Registry) SetGauge(name string, value int64) {
	if r == nil {
		return
	}
	r.gauge(name).Store(value)
}

// AddGauge adjusts a gauge by delta (which may be negative) and returns the new
// value. It is used to track in-flight counts.
func (r *Registry) AddGauge(name string, delta int64) int64 {
	if r == nil {
		return 0
	}
	return r.gauge(name).Add(delta)
}

// Snapshot returns a stable copy of all counters and gauges.
func (r *Registry) Snapshot() Snapshot {
	snapshot := Snapshot{Counters: map[string]uint64{}, Gauges: map[string]int64{}}
	if r == nil {
		return snapshot
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name, value := range r.counters {
		snapshot.Counters[name] = value.Load()
	}
	for name, value := range r.gauges {
		snapshot.Gauges[name] = value.Load()
	}
	return snapshot
}

// Names returns the sorted counter and gauge names currently registered.
func (r *Registry) Names() (counters, gauges []string) {
	if r == nil {
		return nil, nil
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	for name := range r.counters {
		counters = append(counters, name)
	}
	for name := range r.gauges {
		gauges = append(gauges, name)
	}
	sort.Strings(counters)
	sort.Strings(gauges)
	return counters, gauges
}

func (r *Registry) counter(name string) *atomic.Uint64 {
	r.mu.RLock()
	c, ok := r.counters[name]
	r.mu.RUnlock()
	if ok {
		return c
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.counters[name]; ok {
		return c
	}
	c = &atomic.Uint64{}
	r.counters[name] = c
	return c
}

func (r *Registry) gauge(name string) *atomic.Int64 {
	r.mu.RLock()
	g, ok := r.gauges[name]
	r.mu.RUnlock()
	if ok {
		return g
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if g, ok := r.gauges[name]; ok {
		return g
	}
	g = &atomic.Int64{}
	r.gauges[name] = g
	return g
}
