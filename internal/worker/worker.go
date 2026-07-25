// Package worker runs the durable delivery loop: it claims dispatch jobs under
// fencing leases, signs and delivers over the SSRF-safe transport, classifies
// the outcome, applies bounded exponential backoff with jitter and Retry-After,
// circuit-breaks failing endpoints, dead-letters terminal failures, and drains
// the payload-free metadata outbox to a sink. Delivery is at-least-once.
package worker

import (
	"context"
	"errors"
	"log/slog"
	"math/rand"
	"sync"
	"sync/atomic"
	"time"

	"github.com/hookship/hook-service/internal/metrics"
	"github.com/hookship/hook-service/internal/outboxsink"
	"github.com/hookship/hook-service/internal/ratelimit"
	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/signing"
	"github.com/hookship/hook-service/internal/transport"
)

// Options configures the delivery worker.
type Options struct {
	Backend           Backend
	Transport         *transport.Client
	Sink              outboxsink.Sink
	Clock             runtime.Clock
	IDs               runtime.IDSource
	Logger            *slog.Logger
	Metrics           *metrics.Registry
	Limiter           *ratelimit.Limiter
	Concurrency       int
	LeaseDuration     time.Duration
	DeliveryTimeout   time.Duration
	BaseBackoff       time.Duration
	MaxBackoff        time.Duration
	PollInterval      time.Duration
	BackpressureDelay time.Duration
	BreakerThreshold  int
	BreakerCooldown   time.Duration
	// Wake, when set, triggers an immediate claim pass. The relay notifier
	// signals it on each dispatch notification to reduce claim latency.
	Wake <-chan struct{}
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = runtime.SystemClock{}
	}
	if o.IDs == nil {
		o.IDs = runtime.NewULIDSource(o.Clock)
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Sink == nil {
		o.Sink = outboxsink.NoopSink{}
	}
	if o.Concurrency < 1 {
		o.Concurrency = 4
	}
	if o.LeaseDuration <= 0 {
		o.LeaseDuration = 30 * time.Second
	}
	if o.DeliveryTimeout <= 0 {
		o.DeliveryTimeout = 30 * time.Second
	}
	if o.BaseBackoff <= 0 {
		o.BaseBackoff = time.Second
	}
	if o.MaxBackoff <= 0 {
		o.MaxBackoff = 5 * time.Minute
	}
	if o.PollInterval <= 0 {
		o.PollInterval = 250 * time.Millisecond
	}
	if o.BackpressureDelay <= 0 {
		o.BackpressureDelay = time.Second
	}
	return o
}

// Worker owns delivery and outbox drain loops.
type Worker struct {
	options  Options
	owner    string
	breakers *circuitBreakers

	rotation atomic.Uint64
}

// New validates options and constructs a worker.
func New(options Options) (*Worker, error) {
	options = options.withDefaults()
	if options.Backend == nil {
		return nil, errors.New("worker requires a backend")
	}
	if options.Transport == nil {
		return nil, errors.New("worker requires an SSRF-safe transport")
	}
	return &Worker{
		options: options,
		owner:   options.IDs.NewID("wkr"),
		breakers: newCircuitBreakers(breakerConfig{
			failureThreshold: options.BreakerThreshold,
			cooldown:         options.BreakerCooldown,
		}, options.Clock),
	}, nil
}

// Run launches the delivery and outbox loops until the context is canceled. It
// stops claiming before returning so in-flight work drains under its lease.
func (w *Worker) Run(ctx context.Context) error {
	var wait sync.WaitGroup
	for i := 0; i < w.options.Concurrency; i++ {
		wait.Add(1)
		go func() {
			defer wait.Done()
			w.deliveryLoop(ctx)
		}()
	}
	wait.Add(1)
	go func() {
		defer wait.Done()
		w.outboxLoop(ctx)
	}()
	wait.Wait()
	return nil
}

func (w *Worker) deliveryLoop(ctx context.Context) {
	source := rand.New(rand.NewSource(time.Now().UnixNano() ^ int64(w.rotation.Add(1))))
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := w.claimAndDeliverOnce(ctx, source)
		if err != nil && ctx.Err() == nil {
			w.options.Logger.Warn("delivery iteration failed", slog.String("error", err.Error()))
		}
		if !worked {
			if !w.waitForWork(ctx) {
				return
			}
		}
	}
}

// waitForWork sleeps until the poll interval elapses, a relay wake arrives, or
// the context is canceled. It returns false only on cancellation.
func (w *Worker) waitForWork(ctx context.Context) bool {
	timer := time.NewTimer(w.options.PollInterval)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	case <-w.options.Wake:
		return true
	}
}

// claimAndDeliverOnce iterates assignments round-robin for fairness and delivers
// at most one job. It returns whether any job was processed.
func (w *Worker) claimAndDeliverOnce(ctx context.Context, source *rand.Rand) (bool, error) {
	assignments, err := w.options.Backend.Assignments(ctx)
	if err != nil {
		return false, err
	}
	if len(assignments) == 0 {
		return false, nil
	}
	start := int(w.rotation.Add(1) % uint64(len(assignments)))
	for offset := 0; offset < len(assignments); offset++ {
		assignment := assignments[(start+offset)%len(assignments)]
		token := w.options.IDs.NewID("lease")
		job, ok, err := w.options.Backend.ClaimJob(ctx, assignment.ID, runtime.JobKindDispatch, w.owner, token, w.options.LeaseDuration)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		w.deliver(ctx, assignment, job, token, source)
		return true, nil
	}
	return false, nil
}

func (w *Worker) deliver(ctx context.Context, assignment runtime.Assignment, job runtime.Job, token string, source *rand.Rand) {
	lease := runtime.JobLease{AssignmentID: job.AssignmentID, JobID: job.JobID, Owner: w.owner, Token: token, Attempt: job.Attempts}

	breakerKey := job.EndpointSnapshotID
	if !w.breakers.Allow(breakerKey) {
		// The endpoint circuit is open; reschedule without a provider call.
		w.options.Metrics.Inc(metrics.CounterCircuitOpenSkips)
		w.logSettlement("retry_circuit_open", job.JobID, w.options.Backend.RetryJob(ctx, lease, w.breakers.Cooldown(), "circuit_open"))
		return
	}

	// Enforce per-assignment and per-endpoint concurrency and rate limits with
	// bounded backpressure: on rejection the job is rescheduled after a short
	// delay instead of blocking a worker goroutine.
	releaseLimit, reason, ok := w.options.Limiter.Acquire(assignment.ID, breakerKey)
	if !ok {
		switch reason {
		case ratelimit.ReasonAssignmentRate, ratelimit.ReasonEndpointRate:
			w.options.Metrics.Inc(metrics.CounterRateLimited)
		default:
			w.options.Metrics.Inc(metrics.CounterConcurrencyDeferred)
		}
		w.logSettlement("retry_backpressure", job.JobID, w.options.Backend.RetryJob(ctx, lease, w.options.BackpressureDelay, "backpressure_"+string(reason)))
		return
	}
	defer releaseLimit()
	w.options.Metrics.AddGauge(metrics.GaugeInFlightDeliveries, 1)
	defer w.options.Metrics.AddGauge(metrics.GaugeInFlightDeliveries, -1)
	w.options.Metrics.Inc(metrics.CounterDeliveriesAttempted)

	delivery, err := w.options.Backend.LoadDelivery(ctx, job)
	if err != nil {
		w.options.Logger.Warn("load delivery failed", slog.String("job_id", job.JobID), slog.String("error", err.Error()))
		w.logSettlement("retry_load_failed", job.JobID, w.options.Backend.RetryJob(ctx, lease, w.backoff(job.Attempts, source), "load_failed"))
		return
	}

	signer, err := signing.NewSigner(delivery.Endpoint.SigningSecret)
	if err != nil {
		w.logSettlement("fail_invalid_signing_secret", job.JobID, w.options.Backend.FailJob(ctx, lease, "invalid_signing_secret"))
		return
	}
	started := w.options.Clock.Now()
	signature, err := signer.Sign(job.DeliveryID, started, delivery.Body)
	if err != nil {
		w.logSettlement("fail_sign_failed", job.JobID, w.options.Backend.FailJob(ctx, lease, "sign_failed"))
		return
	}

	attemptCtx, cancel := context.WithTimeout(ctx, w.options.DeliveryTimeout)
	result, transportErr := w.options.Transport.Deliver(
		attemptCtx,
		delivery.Endpoint.URL,
		delivery.ContentType,
		signature.Headers(),
		delivery.Body,
	)
	cancel()

	classification := Classify(result, transportErr)
	finished := w.options.Clock.Now()
	w.breakers.Record(breakerKey, classification.Outcome == runtime.OutcomeSuccess)

	w.recordAttempt(ctx, job, started, finished, result, classification)
	w.settle(ctx, lease, job, classification, source)
}

func (w *Worker) settle(ctx context.Context, lease runtime.JobLease, job runtime.Job, classification Classification, source *rand.Rand) {
	switch classification.Outcome {
	case runtime.OutcomeSuccess:
		w.options.Metrics.Inc(metrics.CounterDeliveriesSucceeded)
		w.logSettlement("complete", job.JobID, w.options.Backend.CompleteJob(ctx, lease))
	case runtime.OutcomeTerminal:
		w.options.Metrics.Inc(metrics.CounterDeliveriesDead)
		w.logSettlement("fail_terminal", job.JobID, w.options.Backend.FailJob(ctx, lease, classification.ErrorCode))
	default:
		w.options.Metrics.Inc(metrics.CounterDeliveriesRetried)
		delay := w.backoff(job.Attempts, source)
		if classification.HasRetryAfter && classification.RetryAfter > 0 {
			delay = boundRetryAfter(classification.RetryAfter, w.options.MaxBackoff)
		}
		w.logSettlement("retry", job.JobID, w.options.Backend.RetryJob(ctx, lease, delay, classification.ErrorCode))
	}
}

// logSettlement uniformly surfaces the outcome of a job or outbox settlement or
// reschedule so no settlement error is ever silently dropped. A lost or fenced
// lease is an expected outcome under fencing (another worker won the work, or
// the lease expired) and is logged at debug; any other error is logged at warn.
func (w *Worker) logSettlement(op, workID string, err error) {
	switch {
	case err == nil:
		return
	case errors.Is(err, runtime.ErrLeaseLost):
		w.options.Logger.Debug("settlement skipped: lease lost or fenced",
			slog.String("op", op), slog.String("work_id", workID))
	default:
		w.options.Logger.Warn("settlement failed",
			slog.String("op", op), slog.String("work_id", workID), slog.String("error", err.Error()))
	}
}

func (w *Worker) recordAttempt(ctx context.Context, job runtime.Job, started, finished time.Time, result transport.Result, classification Classification) {
	durationMS := result.DurationMS
	if durationMS <= 0 {
		durationMS = int(finished.Sub(started).Milliseconds())
		if durationMS < 0 {
			durationMS = 0
		}
	}
	attemptID := w.options.IDs.NewID("att")
	attempt := runtime.AttemptRecord{
		AttemptID:          attemptID,
		AssignmentID:       job.AssignmentID,
		DeliveryID:         job.DeliveryID,
		JobID:              job.JobID,
		EndpointSnapshotID: job.EndpointSnapshotID,
		AttemptNumber:      job.Attempts,
		Outcome:            classification.Outcome,
		ResponseStatus:     classification.ResponseCode,
		ErrorCode:          classification.ErrorCode,
		DurationMS:         durationMS,
		StartedAt:          started,
		FinishedAt:         finished,
	}
	observation := runtime.OutboxObservation{
		OutboxID:           w.options.IDs.NewID("obx"),
		AssignmentID:       job.AssignmentID,
		DeliveryID:         job.DeliveryID,
		AttemptID:          attemptID,
		EndpointSnapshotID: job.EndpointSnapshotID,
		SchemaVersion:      "1",
		EventKind:          "delivery.attempted",
		Outcome:            classification.Outcome,
		ResponseStatus:     classification.ResponseCode,
		ErrorCode:          classification.ErrorCode,
		OccurredAt:         finished,
		RunAt:              finished,
		MaxAttempts:        job.MaxAttempts,
	}
	if err := w.options.Backend.RecordAttempt(ctx, attempt, observation); err != nil {
		w.options.Logger.Warn("record attempt failed", slog.String("job_id", job.JobID), slog.String("error", err.Error()))
	}
}

func (w *Worker) backoff(attempt int, source *rand.Rand) time.Duration {
	return Backoff(attempt, w.options.BaseBackoff, w.options.MaxBackoff, source)
}

func boundRetryAfter(value, max time.Duration) time.Duration {
	if value > max {
		return max
	}
	return value
}

func sleep(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}
