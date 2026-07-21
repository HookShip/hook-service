package worker

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hookship/hook-service/internal/outboxsink"
	"github.com/hookship/hook-service/internal/runtime"
)

// outboxLoop drains the payload-free metadata outbox to the configured sink. A
// publish failure reschedules the row idempotently; PostgreSQL remains the
// source of truth, so a sink outage never loses observations.
func (w *Worker) outboxLoop(ctx context.Context) {
	for {
		if ctx.Err() != nil {
			return
		}
		worked, err := w.drainOutboxOnce(ctx)
		if err != nil && ctx.Err() == nil {
			w.options.Logger.Warn("outbox iteration failed", slog.String("error", err.Error()))
		}
		if !worked {
			if !sleep(ctx, w.options.PollInterval) {
				return
			}
		}
	}
}

func (w *Worker) drainOutboxOnce(ctx context.Context) (bool, error) {
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
		token := w.options.IDs.NewID("olease")
		item, ok, err := w.options.Backend.ClaimOutbox(ctx, assignment.ID, w.owner, token, w.options.LeaseDuration)
		if err != nil {
			return false, err
		}
		if !ok {
			continue
		}
		w.publishOutbox(ctx, item, token)
		return true, nil
	}
	return false, nil
}

func (w *Worker) publishOutbox(ctx context.Context, item runtime.OutboxItem, token string) {
	lease := runtime.OutboxLease{AssignmentID: item.AssignmentID, OutboxID: item.OutboxID, Owner: w.owner, Token: token, Attempt: item.Attempts}
	observation := outboxsink.Observation{
		SchemaVersion:      item.SchemaVersion,
		AssignmentID:       item.AssignmentID,
		DeliveryID:         item.DeliveryID,
		AttemptID:          item.AttemptID,
		EndpointSnapshotID: item.EndpointSnapshotID,
		EventKind:          item.EventKind,
		Outcome:            string(item.Outcome),
		ResponseStatus:     item.ResponseStatus,
		ErrorCode:          item.ErrorCode,
		OccurredAt:         item.OccurredAt,
	}
	publishCtx, cancel := context.WithTimeout(ctx, w.options.DeliveryTimeout)
	err := w.options.Sink.Publish(publishCtx, observation)
	cancel()
	if err != nil {
		if retryErr := w.options.Backend.RetryOutbox(ctx, lease, 5*time.Second, "sink_error"); retryErr != nil && !errors.Is(retryErr, runtime.ErrLeaseLost) {
			w.options.Logger.Warn("retry outbox failed", slog.String("outbox_id", item.OutboxID), slog.String("error", retryErr.Error()))
		}
		return
	}
	if err := w.options.Backend.PublishOutbox(ctx, lease); err != nil && !errors.Is(err, runtime.ErrLeaseLost) {
		w.options.Logger.Warn("publish outbox failed", slog.String("outbox_id", item.OutboxID), slog.String("error", err.Error()))
	}
}
