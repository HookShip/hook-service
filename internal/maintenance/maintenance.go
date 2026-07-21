// Package maintenance runs the durable retention and cleanup workers. On a
// bounded interval it purges expired replay nonces, advances committed
// idempotency ledgers to tombstones, orphans expired object-store payload
// stages, reclaims expired payload ciphertext (both PostgreSQL bytes and
// immutable objects), and purges terminal metadata-outbox rows. Every operation
// is bounded per pass so retention never monopolizes the database, and all
// counts are reported to the metrics registry for operator visibility.
package maintenance

import (
	"context"
	"log/slog"
	"time"

	"github.com/hookship/hook-service/internal/metrics"
	"github.com/hookship/hook-service/internal/runtime"
)

// RetentionBackend is the durable-backend surface the maintenance worker needs.
// The PostgreSQL backend implements it; ephemeral backends do not and are simply
// not given a maintenance worker.
type RetentionBackend interface {
	Assignments(ctx context.Context) ([]runtime.Assignment, error)
	PurgeExpiredNonces(ctx context.Context, limit int) (int64, error)
	AdvanceTombstones(ctx context.Context, assignmentID string, limit int) (int64, error)
	MarkExpiredObjectStagesOrphaned(ctx context.Context, assignmentID string, limit int) (int64, error)
	ReclaimExpiredPayloads(ctx context.Context, assignmentID string, limit int) (int, error)
	PurgeTerminalOutbox(ctx context.Context, assignmentID string, olderThan time.Time, limit int) (int64, error)
}

// Options configures the maintenance worker.
type Options struct {
	Backend         RetentionBackend
	Clock           runtime.Clock
	Logger          *slog.Logger
	Metrics         *metrics.Registry
	Interval        time.Duration
	BatchLimit      int
	OutboxRetention time.Duration
}

func (o Options) withDefaults() Options {
	if o.Clock == nil {
		o.Clock = runtime.SystemClock{}
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Interval <= 0 {
		o.Interval = time.Minute
	}
	if o.BatchLimit <= 0 || o.BatchLimit > 10000 {
		o.BatchLimit = 500
	}
	if o.OutboxRetention <= 0 {
		o.OutboxRetention = 24 * time.Hour
	}
	return o
}

// Worker runs periodic retention passes.
type Worker struct {
	options Options
}

// New validates options and builds a maintenance worker.
func New(options Options) (*Worker, error) {
	if options.Backend == nil {
		return nil, errNoBackend
	}
	return &Worker{options: options.withDefaults()}, nil
}

var errNoBackend = errString("maintenance worker requires a retention backend")

type errString string

func (e errString) Error() string { return string(e) }

// Run executes retention passes until the context is canceled. It runs one pass
// immediately, then on each interval tick.
func (w *Worker) Run(ctx context.Context) error {
	ticker := time.NewTicker(w.options.Interval)
	defer ticker.Stop()
	w.runOnce(ctx)
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			w.runOnce(ctx)
		}
	}
}

// RunOnce executes a single retention pass. It is exported for tests and for
// operator-triggered cleanup.
func (w *Worker) RunOnce(ctx context.Context) {
	w.runOnce(ctx)
}

func (w *Worker) runOnce(ctx context.Context) {
	limit := w.options.BatchLimit
	if purged, err := w.options.Backend.PurgeExpiredNonces(ctx, limit); err != nil {
		w.recordError(ctx, "purge expired nonces", err)
	} else {
		w.options.Metrics.Add(metrics.CounterNoncesPurged, uint64(purged))
	}

	assignments, err := w.options.Backend.Assignments(ctx)
	if err != nil {
		w.recordError(ctx, "list assignments for retention", err)
		return
	}
	outboxCutoff := w.options.Clock.Now().Add(-w.options.OutboxRetention)
	for _, assignment := range assignments {
		if ctx.Err() != nil {
			return
		}
		if advanced, err := w.options.Backend.AdvanceTombstones(ctx, assignment.ID, limit); err != nil {
			w.recordError(ctx, "advance tombstones", err)
		} else {
			w.options.Metrics.Add(metrics.CounterTombstones, uint64(advanced))
		}
		if orphaned, err := w.options.Backend.MarkExpiredObjectStagesOrphaned(ctx, assignment.ID, limit); err != nil {
			w.recordError(ctx, "orphan object stages", err)
		} else {
			w.options.Metrics.Add(metrics.CounterObjectsOrphaned, uint64(orphaned))
		}
		if reclaimed, err := w.options.Backend.ReclaimExpiredPayloads(ctx, assignment.ID, limit); err != nil {
			w.recordError(ctx, "reclaim expired payloads", err)
		} else {
			w.options.Metrics.AddInt(metrics.CounterPayloadsReclaimed, reclaimed)
		}
		if purged, err := w.options.Backend.PurgeTerminalOutbox(ctx, assignment.ID, outboxCutoff, limit); err != nil {
			w.recordError(ctx, "purge terminal outbox", err)
		} else {
			w.options.Metrics.Add(metrics.CounterOutboxPurged, uint64(purged))
		}
	}
}

func (w *Worker) recordError(ctx context.Context, operation string, err error) {
	if ctx.Err() != nil {
		return
	}
	w.options.Metrics.Inc(metrics.CounterRetentionErrors)
	w.options.Logger.Warn("retention pass operation failed",
		slog.String("operation", operation),
		slog.String("error", err.Error()),
	)
}
