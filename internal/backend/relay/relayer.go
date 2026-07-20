package relay

import (
	"context"
	"log/slog"
	"sync"
	"time"
)

// SignalSource lists advisory dispatch signals derived from committed durable
// jobs. It never mutates state.
type SignalSource func(ctx context.Context, limit int) ([]Signal, error)

// RelayerOptions configures the outbound relayer.
type RelayerOptions struct {
	Source    SignalSource
	Publisher Publisher
	Interval  time.Duration
	Batch     int
	DedupTTL  time.Duration
	Logger    *slog.Logger
}

// Relayer periodically reads claimable durable jobs and publishes advisory
// dispatch signals to the transport. It is the "outbox → stream" half: signals
// are produced only from committed PostgreSQL state, so there is no dual write,
// and a crash simply re-reads the same claimable jobs (replay).
type Relayer struct {
	options RelayerOptions

	mu     sync.Mutex
	recent map[string]time.Time
}

// NewRelayer builds a relayer.
func NewRelayer(options RelayerOptions) *Relayer {
	if options.Interval <= 0 {
		options.Interval = 250 * time.Millisecond
	}
	if options.Batch <= 0 {
		options.Batch = 256
	}
	if options.DedupTTL <= 0 {
		options.DedupTTL = 2 * time.Second
	}
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Relayer{options: options, recent: make(map[string]time.Time)}
}

// Run publishes dispatch signals until the context is canceled.
func (r *Relayer) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.options.Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.publishBatch(ctx)
		}
	}
}

func (r *Relayer) publishBatch(ctx context.Context) {
	signals, err := r.options.Source(ctx, r.options.Batch)
	if err != nil {
		if ctx.Err() == nil {
			r.options.Logger.Warn("relay source failed", slog.String("error", err.Error()))
		}
		return
	}
	now := time.Now()
	r.evictRecent(now)
	for _, signal := range signals {
		if r.recentlyPublished(signal.JobID, now) {
			continue
		}
		if err := r.options.Publisher.Publish(ctx, signal); err != nil {
			if ctx.Err() == nil {
				r.options.Logger.Warn("relay publish failed", slog.String("error", err.Error()))
			}
			// Loss is recoverable: the job stays in PostgreSQL and is re-read.
			return
		}
		r.markPublished(signal.JobID, now)
	}
}

func (r *Relayer) recentlyPublished(jobID string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	until, ok := r.recent[jobID]
	return ok && until.After(now)
}

func (r *Relayer) markPublished(jobID string, now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.recent[jobID] = now.Add(r.options.DedupTTL)
}

func (r *Relayer) evictRecent(now time.Time) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.recent) < 10000 {
		for id, until := range r.recent {
			if !until.After(now) {
				delete(r.recent, id)
			}
		}
		return
	}
	// Hard reset if the dedup set grows unexpectedly large.
	r.recent = make(map[string]time.Time)
}

// NotifierOptions configures the inbound notifier.
type NotifierOptions struct {
	Consumer Consumer
	// Wake is signaled (non-blocking) for each consumed dispatch signal so the
	// worker performs an immediate claim pass.
	Wake   chan<- struct{}
	Logger *slog.Logger
}

// Notifier consumes dispatch signals and wakes the worker. It is the
// "stream → claim" half; PostgreSQL remains authoritative, so a spurious or
// duplicate wake merely triggers a harmless idempotent claim.
type Notifier struct {
	options NotifierOptions
}

// NewNotifier builds a notifier.
func NewNotifier(options NotifierOptions) *Notifier {
	if options.Logger == nil {
		options.Logger = slog.Default()
	}
	return &Notifier{options: options}
}

// Run consumes signals until the context is canceled.
func (n *Notifier) Run(ctx context.Context) error {
	return n.options.Consumer.Consume(ctx, func(_ context.Context, _ Signal) error {
		select {
		case n.options.Wake <- struct{}{}:
		default:
		}
		return nil
	})
}
