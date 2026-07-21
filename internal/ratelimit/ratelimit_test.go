package ratelimit

import (
	"sync"
	"testing"
	"time"
)

type manualClock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.t
}

func (c *manualClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

func TestDisabledLimiterAlwaysAllows(t *testing.T) {
	l := New(Config{}, &manualClock{t: time.Now()})
	for i := 0; i < 100; i++ {
		release, reason, ok := l.Acquire("asg", "ep")
		if !ok || reason != ReasonNone {
			t.Fatalf("disabled limiter must always allow, got %v %v", ok, reason)
		}
		release()
	}
}

func TestEndpointConcurrencyLimit(t *testing.T) {
	l := New(Config{EndpointConcurrency: 2}, &manualClock{t: time.Now()})
	r1, _, ok1 := l.Acquire("asg", "ep")
	r2, _, ok2 := l.Acquire("asg", "ep")
	_, reason, ok3 := l.Acquire("asg", "ep")
	if !ok1 || !ok2 {
		t.Fatal("first two acquisitions should succeed")
	}
	if ok3 || reason != ReasonEndpointConcurrency {
		t.Fatalf("third acquisition should hit endpoint concurrency, got ok=%v reason=%v", ok3, reason)
	}
	// Releasing frees a slot.
	r1()
	r4, _, ok4 := l.Acquire("asg", "ep")
	if !ok4 {
		t.Fatal("acquisition after release should succeed")
	}
	r2()
	r4()
}

func TestAssignmentConcurrencyIndependentOfEndpoint(t *testing.T) {
	l := New(Config{AssignmentConcurrency: 1}, &manualClock{t: time.Now()})
	r1, _, ok1 := l.Acquire("asg", "ep-a")
	_, reason, ok2 := l.Acquire("asg", "ep-b")
	if !ok1 {
		t.Fatal("first acquisition should succeed")
	}
	if ok2 || reason != ReasonAssignmentConcurrency {
		t.Fatalf("second acquisition on same assignment should be limited, got ok=%v reason=%v", ok2, reason)
	}
	r1()
}

func TestEndpointRateLimitRefills(t *testing.T) {
	clock := &manualClock{t: time.Unix(0, 0)}
	l := New(Config{EndpointRatePerSec: 10, EndpointBurst: 2}, clock)
	// Burst of 2 tokens.
	if r, _, ok := l.Acquire("asg", "ep"); !ok {
		t.Fatal("first token")
	} else {
		r()
	}
	if r, _, ok := l.Acquire("asg", "ep"); !ok {
		t.Fatal("second token")
	} else {
		r()
	}
	if _, reason, ok := l.Acquire("asg", "ep"); ok || reason != ReasonEndpointRate {
		t.Fatalf("third acquire should be rate limited, got ok=%v reason=%v", ok, reason)
	}
	// After 0.1s at 10/s, one token refills.
	clock.advance(100 * time.Millisecond)
	if r, _, ok := l.Acquire("asg", "ep"); !ok {
		t.Fatal("token should refill after elapsed time")
	} else {
		r()
	}
}

func TestRejectedAcquireDoesNotConsumeTokenOrSlot(t *testing.T) {
	clock := &manualClock{t: time.Unix(0, 0)}
	// Endpoint concurrency 0 (unlimited), assignment concurrency 1, endpoint rate
	// with a burst of 2 so one token survives the first acquire.
	l := New(Config{AssignmentConcurrency: 1, EndpointRatePerSec: 1, EndpointBurst: 2}, clock)
	r1, _, ok1 := l.Acquire("asg", "ep")
	if !ok1 {
		t.Fatal("first acquire should succeed")
	}
	// Second acquire fails on assignment concurrency; it must not consume the
	// surviving endpoint rate token.
	if _, reason, ok := l.Acquire("asg", "ep"); ok || reason != ReasonAssignmentConcurrency {
		t.Fatalf("expected assignment concurrency rejection, got ok=%v reason=%v", ok, reason)
	}
	r1()
	// The surviving endpoint rate token was not consumed by the rejected acquire,
	// so this succeeds immediately without waiting for a refill.
	if r, _, ok := l.Acquire("asg", "ep"); !ok {
		t.Fatal("endpoint token must survive a concurrency rejection")
	} else {
		r()
	}
}
