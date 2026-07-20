package postgres

import (
	"github.com/hookship/hook-service/internal/runtime"

	durable "github.com/hookship/hook-service/internal/durability/postgres"
)

func toRuntimeReservation(reservation durable.Reservation) runtime.Reservation {
	return runtime.Reservation{
		LedgerID:             reservation.LedgerID,
		AssignmentID:         reservation.AssignmentID,
		IdempotencyKey:       reservation.IdempotencyKey,
		BodySHA256:           runtime.Hash(reservation.BodySHA256),
		Headers:              runtime.StableHeaders(reservation.Headers),
		State:                reservation.State,
		IngestID:             reservation.IngestID,
		PayloadID:            reservation.PayloadID,
		DispatchJobID:        reservation.DispatchJobID,
		ReservedAt:           reservation.ReservedAt,
		AcceptedAt:           reservation.AcceptedAt,
		IdempotencyExpiresAt: reservation.IdempotencyExpiresAt,
	}
}

func toRuntimeJob(job durable.Job) runtime.Job {
	return runtime.Job{
		JobID:              job.JobID,
		AssignmentID:       job.AssignmentID,
		Kind:               job.Kind,
		IngestID:           job.IngestID,
		PayloadID:          job.PayloadID,
		DeliveryID:         job.DeliveryID,
		EndpointSnapshotID: job.EndpointSnapshotID,
		State:              job.State,
		RunAt:              job.RunAt,
		LeaseExpiresAt:     job.LeaseExpiresAt,
		Attempts:           job.Attempts,
		MaxAttempts:        job.MaxAttempts,
		LastErrorCode:      job.LastErrorCode,
	}
}

func toRuntimeOutboxItem(item durable.OutboxItem) runtime.OutboxItem {
	return runtime.OutboxItem{
		OutboxObservation: runtime.OutboxObservation{
			OutboxID:           item.OutboxID,
			AssignmentID:       item.AssignmentID,
			DeliveryID:         item.DeliveryID,
			AttemptID:          item.AttemptID,
			EndpointSnapshotID: item.EndpointSnapshotID,
			SchemaVersion:      item.SchemaVersion,
			EventKind:          item.EventKind,
			Outcome:            runtime.AttemptOutcome(item.Outcome),
			ResponseStatus:     item.ResponseStatus,
			ErrorCode:          item.ErrorCode,
			OccurredAt:         item.OccurredAt,
			RunAt:              item.RunAt,
			MaxAttempts:        item.MaxAttempts,
		},
		State:          item.State,
		LeaseExpiresAt: item.LeaseExpiresAt,
		Attempts:       item.Attempts,
		LastErrorCode:  item.LastErrorCode,
	}
}

func toDurableAttempt(attempt runtime.AttemptRecord) durable.AttemptRecord {
	return durable.AttemptRecord{
		AttemptID:          attempt.AttemptID,
		AssignmentID:       attempt.AssignmentID,
		DeliveryID:         attempt.DeliveryID,
		JobID:              attempt.JobID,
		EndpointSnapshotID: attempt.EndpointSnapshotID,
		AttemptNumber:      attempt.AttemptNumber,
		Outcome:            string(attempt.Outcome),
		ResponseStatus:     attempt.ResponseStatus,
		ErrorCode:          attempt.ErrorCode,
		DurationMS:         attempt.DurationMS,
		StartedAt:          attempt.StartedAt,
		FinishedAt:         attempt.FinishedAt,
	}
}

func toDurableOutbox(observation runtime.OutboxObservation) durable.OutboxRecord {
	return durable.OutboxRecord{
		OutboxID:           observation.OutboxID,
		AssignmentID:       observation.AssignmentID,
		DeliveryID:         observation.DeliveryID,
		AttemptID:          observation.AttemptID,
		EndpointSnapshotID: observation.EndpointSnapshotID,
		SchemaVersion:      observation.SchemaVersion,
		EventKind:          observation.EventKind,
		Outcome:            string(observation.Outcome),
		ResponseStatus:     observation.ResponseStatus,
		ErrorCode:          observation.ErrorCode,
		OccurredAt:         observation.OccurredAt,
		RunAt:              observation.RunAt,
		MaxAttempts:        observation.MaxAttempts,
	}
}
