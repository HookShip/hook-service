// Package ratelimit provides per-assignment and per-endpoint concurrency limits
// and token-bucket rate limits with bounded, non-blocking backpressure. The
// delivery worker consults a Limiter before each attempt; when a limit is hit
// the worker defers the job with a short retry instead of blocking, so one busy
// endpoint cannot starve the shared worker pool.
package ratelimit

import (
	"sync"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

// Config bounds delivery concurrency and rate per assignment and per endpoint.
// A zero value for a field disables that particular limit.
type Config struct {
	// AssignmentConcurrency caps concurrent in-flight deliveries per assignment.
	AssignmentConcurrency int
	// EndpointConcurrency caps concurrent in-flight deliveries per endpoint.
	EndpointConcurrency int
	// AssignmentRatePerSec and AssignmentBurst bound the per-assignment delivery
	// rate with a token bucket.
	AssignmentRatePerSec float64
	AssignmentBurst      int
	// EndpointRatePerSec and EndpointBurst bound the per-endpoint delivery rate.
	EndpointRatePerSec float64
	EndpointBurst      int
}

// Enabled reports whether any limit is configured.
func (c Config) Enabled() bool {
	return c.AssignmentConcurrency > 0 || c.EndpointConcurrency > 0 ||
		c.AssignmentRatePerSec > 0 || c.EndpointRatePerSec > 0
}

// Reason identifies which limit rejected an acquisition.
type Reason string

const (
	ReasonNone                  Reason = ""
	ReasonAssignmentConcurrency Reason = "assignment_concurrency"
	ReasonEndpointConcurrency   Reason = "endpoint_concurrency"
	ReasonAssignmentRate        Reason = "assignment_rate"
	ReasonEndpointRate          Reason = "endpoint_rate"
)

// Limiter enforces the configured limits. It is safe for concurrent use.
type Limiter struct {
	cfg   Config
	clock runtime.Clock

	mu       sync.Mutex
	inFlight map[string]int
	buckets  map[string]*bucket
}

type bucket struct {
	tokens     float64
	lastRefill time.Time
}

// New builds a limiter. A nil clock uses the system clock.
func New(cfg Config, clock runtime.Clock) *Limiter {
	if clock == nil {
		clock = runtime.SystemClock{}
	}
	return &Limiter{
		cfg:      cfg,
		clock:    clock,
		inFlight: make(map[string]int),
		buckets:  make(map[string]*bucket),
	}
}

// Acquire reserves one delivery slot for (assignmentID, endpointKey). On success
// it returns a release function that must be called when the attempt finishes.
// On failure it returns the limiting reason and ok=false without reserving
// anything, so the caller can apply bounded backpressure.
func (l *Limiter) Acquire(assignmentID, endpointKey string) (release func(), reason Reason, ok bool) {
	if l == nil || !l.cfg.Enabled() {
		return func() {}, ReasonNone, true
	}
	asgKey := "a:" + assignmentID
	epKey := "e:" + endpointKey

	l.mu.Lock()
	defer l.mu.Unlock()

	if l.cfg.AssignmentConcurrency > 0 && l.inFlight[asgKey] >= l.cfg.AssignmentConcurrency {
		return nil, ReasonAssignmentConcurrency, false
	}
	if l.cfg.EndpointConcurrency > 0 && l.inFlight[epKey] >= l.cfg.EndpointConcurrency {
		return nil, ReasonEndpointConcurrency, false
	}

	now := l.clock.Now()
	if l.cfg.AssignmentRatePerSec > 0 && !l.peekToken(asgKey, l.cfg.AssignmentRatePerSec, l.cfg.AssignmentBurst, now) {
		return nil, ReasonAssignmentRate, false
	}
	if l.cfg.EndpointRatePerSec > 0 && !l.peekToken(epKey, l.cfg.EndpointRatePerSec, l.cfg.EndpointBurst, now) {
		return nil, ReasonEndpointRate, false
	}
	// Both rate buckets have a token available; consume them now that concurrency
	// checks also passed, so a rejected acquire never consumes a token.
	if l.cfg.AssignmentRatePerSec > 0 {
		l.buckets[asgKey].tokens--
	}
	if l.cfg.EndpointRatePerSec > 0 {
		l.buckets[epKey].tokens--
	}
	if l.cfg.AssignmentConcurrency > 0 {
		l.inFlight[asgKey]++
	}
	if l.cfg.EndpointConcurrency > 0 {
		l.inFlight[epKey]++
	}

	released := false
	return func() {
		l.mu.Lock()
		defer l.mu.Unlock()
		if released {
			return
		}
		released = true
		if l.cfg.AssignmentConcurrency > 0 && l.inFlight[asgKey] > 0 {
			l.inFlight[asgKey]--
		}
		if l.cfg.EndpointConcurrency > 0 && l.inFlight[epKey] > 0 {
			l.inFlight[epKey]--
		}
	}, ReasonNone, true
}

// peekToken refills the bucket for key and reports whether a token is available
// without consuming it. The caller consumes it only after all checks pass.
func (l *Limiter) peekToken(key string, ratePerSec float64, burst int, now time.Time) bool {
	if burst < 1 {
		burst = 1
	}
	b, ok := l.buckets[key]
	if !ok {
		b = &bucket{tokens: float64(burst), lastRefill: now}
		l.buckets[key] = b
	}
	if elapsed := now.Sub(b.lastRefill).Seconds(); elapsed > 0 {
		b.tokens += elapsed * ratePerSec
		if b.tokens > float64(burst) {
			b.tokens = float64(burst)
		}
		b.lastRefill = now
	}
	return b.tokens >= 1
}
