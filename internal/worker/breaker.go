package worker

import (
	"sync"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

type breakerState int

const (
	breakerClosed breakerState = iota
	breakerOpen
	breakerHalfOpen
)

type breakerEntry struct {
	state            breakerState
	failures         int
	openedAt         time.Time
	halfOpenInFlight bool
}

// breakerConfig bounds circuit-breaking behavior per endpoint.
type breakerConfig struct {
	failureThreshold int
	cooldown         time.Duration
}

// circuitBreakers tracks per-endpoint circuit state so a persistently failing
// endpoint stops receiving attempts without losing durable jobs (jobs are
// rescheduled, not dropped).
type circuitBreakers struct {
	config breakerConfig
	clock  runtime.Clock

	mu      sync.Mutex
	entries map[string]*breakerEntry
}

func newCircuitBreakers(config breakerConfig, clock runtime.Clock) *circuitBreakers {
	if config.failureThreshold < 1 {
		config.failureThreshold = 5
	}
	if config.cooldown <= 0 {
		config.cooldown = 30 * time.Second
	}
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	return &circuitBreakers{config: config, clock: clock, entries: make(map[string]*breakerEntry)}
}

// Allow reports whether an attempt to the key may proceed. When the breaker is
// open but the cooldown has elapsed it transitions to half-open and admits a
// single trial attempt.
func (c *circuitBreakers) Allow(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		return true
	}
	switch entry.state {
	case breakerClosed:
		return true
	case breakerOpen:
		if c.clock.Now().Sub(entry.openedAt) >= c.config.cooldown {
			entry.state = breakerHalfOpen
			entry.halfOpenInFlight = true
			return true
		}
		return false
	case breakerHalfOpen:
		if entry.halfOpenInFlight {
			return false
		}
		entry.halfOpenInFlight = true
		return true
	}
	return true
}

// Cooldown returns the configured cooldown, used to schedule a retry when an
// attempt is blocked by an open breaker.
func (c *circuitBreakers) Cooldown() time.Duration {
	return c.config.cooldown
}

// Record updates breaker state after an attempt settles.
func (c *circuitBreakers) Record(key string, success bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	if entry == nil {
		entry = &breakerEntry{}
		c.entries[key] = entry
	}
	entry.halfOpenInFlight = false
	if success {
		entry.state = breakerClosed
		entry.failures = 0
		return
	}
	entry.failures++
	if entry.state == breakerHalfOpen || entry.failures >= c.config.failureThreshold {
		entry.state = breakerOpen
		entry.openedAt = c.clock.Now()
	}
}

// Open reports whether the breaker for a key is currently open (for tests and
// diagnostics).
func (c *circuitBreakers) Open(key string) bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	entry := c.entries[key]
	return entry != nil && entry.state == breakerOpen
}
