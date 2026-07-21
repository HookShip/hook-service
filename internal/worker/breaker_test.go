package worker

import (
	"math/rand"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/transport"
)

type manualClock struct{ t time.Time }

func (c *manualClock) Now() time.Time { return c.t }

func TestBreakerOpensAndRecovers(t *testing.T) {
	clock := &manualClock{t: time.Unix(1_700_000_000, 0)}
	breakers := newCircuitBreakers(breakerConfig{failureThreshold: 3, cooldown: 10 * time.Second}, clock)
	key := "snap_endpoint"

	for i := 0; i < 3; i++ {
		if !breakers.Allow(key) {
			t.Fatalf("attempt %d should be allowed before threshold", i)
		}
		breakers.Record(key, false)
	}
	if !breakers.Open(key) {
		t.Fatal("breaker should be open after reaching the failure threshold")
	}
	if breakers.Allow(key) {
		t.Fatal("open breaker should reject attempts during cooldown")
	}

	// After the cooldown a single half-open trial is admitted.
	clock.t = clock.t.Add(11 * time.Second)
	if !breakers.Allow(key) {
		t.Fatal("breaker should admit a half-open trial after cooldown")
	}
	if breakers.Allow(key) {
		t.Fatal("only one half-open trial should be admitted")
	}
	breakers.Record(key, true)
	if breakers.Open(key) {
		t.Fatal("a successful trial should close the breaker")
	}
	if !breakers.Allow(key) {
		t.Fatal("closed breaker should allow attempts")
	}
}

func TestClassifyOutcomes(t *testing.T) {
	cases := []struct {
		name    string
		result  transport.Result
		err     error
		outcome runtime.AttemptOutcome
	}{
		{"2xx success", transport.Result{StatusCode: 200}, nil, runtime.OutcomeSuccess},
		{"500 retry", transport.Result{StatusCode: 500}, nil, runtime.OutcomeRetry},
		{"429 retry", transport.Result{StatusCode: 429}, nil, runtime.OutcomeRetry},
		{"408 retry", transport.Result{StatusCode: 408}, nil, runtime.OutcomeRetry},
		{"400 terminal", transport.Result{StatusCode: 400}, nil, runtime.OutcomeTerminal},
		{"404 terminal", transport.Result{StatusCode: 404}, nil, runtime.OutcomeTerminal},
		{"transport error retry", transport.Result{}, errTransport, runtime.OutcomeRetry},
		{"blocked terminal", transport.Result{}, transport.ErrBlockedAddress, runtime.OutcomeTerminal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := Classify(tc.result, tc.err)
			if got.Outcome != tc.outcome {
				t.Fatalf("outcome = %s, want %s", got.Outcome, tc.outcome)
			}
		})
	}
}

func TestClassifyHonorsRetryAfter(t *testing.T) {
	result := transport.Result{StatusCode: 429, RetryAfter: 42 * time.Second, HasRetryAfter: true}
	got := Classify(result, nil)
	if !got.HasRetryAfter || got.RetryAfter != 42*time.Second {
		t.Fatalf("expected Retry-After propagation, got %+v", got)
	}
}

func TestBackoffIsBoundedAndJittered(t *testing.T) {
	source := rand.New(rand.NewSource(1))
	for attempt := 1; attempt <= 20; attempt++ {
		delay := Backoff(attempt, time.Second, 30*time.Second, source)
		if delay < 0 || delay > 30*time.Second {
			t.Fatalf("attempt %d delay %s out of bounds", attempt, delay)
		}
	}
}

var errTransport = &transportError{}

type transportError struct{}

func (*transportError) Error() string { return "connection reset" }
