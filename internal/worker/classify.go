package worker

import (
	"errors"
	"math/rand"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/transport"
)

// Classification is the decision derived from one delivery attempt.
type Classification struct {
	Outcome       runtime.AttemptOutcome
	ErrorCode     string
	ResponseCode  *int
	RetryAfter    time.Duration
	HasRetryAfter bool
}

// Classify maps a transport result and error to a retry decision. Delivery is
// at-least-once: an ambiguous failure is retried, never treated as delivered.
func Classify(result transport.Result, transportErr error) Classification {
	if transportErr != nil {
		if errors.Is(transportErr, transport.ErrBlockedAddress) {
			// A destination that resolves only to a blocked address is a policy
			// failure; retrying cannot succeed, so dead-letter it.
			return Classification{Outcome: runtime.OutcomeTerminal, ErrorCode: "blocked_address"}
		}
		// Connection resets, timeouts, and TLS errors are transient.
		return Classification{Outcome: runtime.OutcomeRetry, ErrorCode: "transport_error"}
	}

	status := result.StatusCode
	classification := Classification{ResponseCode: &status}
	if result.HasRetryAfter {
		classification.RetryAfter = result.RetryAfter
		classification.HasRetryAfter = true
	}

	switch {
	case status >= 200 && status < 300:
		classification.Outcome = runtime.OutcomeSuccess
		classification.ErrorCode = ""
	case status == 408 || status == 425 || status == 429 || (status >= 500 && status <= 599):
		classification.Outcome = runtime.OutcomeRetry
		classification.ErrorCode = httpErrorCode(status)
	default:
		classification.Outcome = runtime.OutcomeTerminal
		classification.ErrorCode = httpErrorCode(status)
	}
	return classification
}

func httpErrorCode(status int) string {
	switch {
	case status == 408:
		return "http_408_timeout"
	case status == 425:
		return "http_425_too_early"
	case status == 429:
		return "http_429_throttled"
	case status >= 500:
		return "http_5xx"
	default:
		return "http_4xx"
	}
}

// Backoff computes a bounded exponential delay with full jitter for the given
// attempt number (1-based). It never exceeds max.
func Backoff(attempt int, base, max time.Duration, source *rand.Rand) time.Duration {
	if base <= 0 {
		base = time.Second
	}
	if max <= 0 {
		max = 5 * time.Minute
	}
	if attempt < 1 {
		attempt = 1
	}
	// Exponential growth capped to avoid overflow.
	ceiling := base
	for i := 1; i < attempt && ceiling < max; i++ {
		ceiling *= 2
		if ceiling >= max {
			ceiling = max
			break
		}
	}
	if ceiling > max {
		ceiling = max
	}
	// Full jitter in [base/2, ceiling].
	minimum := base / 2
	if minimum <= 0 {
		minimum = base
	}
	span := ceiling - minimum
	if span <= 0 {
		return ceiling
	}
	jitter := time.Duration(source.Int63n(int64(span)))
	return minimum + jitter
}
