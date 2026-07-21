package ingest

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/hookship/hook-service/internal/runtime"
)

type receiptData struct {
	ingestID           string
	requestID          string
	stable             runtime.StableHeaders
	bodyHashHex        string
	acceptedAt         time.Time
	idempotencyExpires time.Time
	duplicate          bool
}

// durableReceipt is the exact protocol-v1 receipt body. Its field set and
// constants match spec/v1/schemas/event-ingest-receipt.schema.json.
type durableReceipt struct {
	SchemaVersion        string `json:"schemaVersion"`
	IngestID             string `json:"ingestId"`
	EventID              string `json:"eventId"`
	EventType            string `json:"eventType"`
	Route                string `json:"route"`
	ContentType          string `json:"contentType"`
	BodySHA256           string `json:"bodySha256"`
	AcceptedAt           string `json:"acceptedAt"`
	IdempotencyExpiresAt string `json:"idempotencyExpiresAt"`
	IdempotencyState     string `json:"idempotencyState"`
	State                string `json:"state"`
	Duplicate            bool   `json:"duplicate"`
	StatusPath           string `json:"statusPath"`
}

// evaluationReceipt is a deliberately distinct body for the ephemeral backend.
// It never claims protocol-v1 durability; it marks the acceptance as ephemeral
// and evaluation-only so it cannot be mistaken for a durable receipt.
type evaluationReceipt struct {
	SchemaVersion        string `json:"schemaVersion"`
	IngestID             string `json:"ingestId"`
	EventID              string `json:"eventId"`
	EventType            string `json:"eventType"`
	Route                string `json:"route"`
	ContentType          string `json:"contentType"`
	BodySHA256           string `json:"bodySha256"`
	AcceptedAt           string `json:"acceptedAt"`
	IdempotencyExpiresAt string `json:"idempotencyExpiresAt"`
	IdempotencyState     string `json:"idempotencyState"`
	State                string `json:"state"`
	Durability           string `json:"durability"`
	Evaluation           bool   `json:"evaluation"`
	Duplicate            bool   `json:"duplicate"`
	StatusPath           string `json:"statusPath"`
	Warning              string `json:"warning"`
}

func (h *Handler) writeReceipt(w http.ResponseWriter, r *http.Request, statusCode int, data receiptData) {
	statusPath := ingestPath + "/" + data.ingestID
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Location", statusPath)
	header.Set("Hookship-Request-Id", data.requestID)
	header.Set("Hookship-Ingest-Id", data.ingestID)
	header.Set("Hookship-Duplicate", boolString(data.duplicate))
	header.Set("Hookship-Idempotency-Expires-At", data.idempotencyExpires.Format(TimestampLayout))

	if h.backend.Durable() {
		w.WriteHeader(statusCode)
		_ = json.NewEncoder(w).Encode(durableReceipt{
			SchemaVersion:        "1",
			IngestID:             data.ingestID,
			EventID:              data.stable.EventID,
			EventType:            data.stable.EventType,
			Route:                data.stable.Route,
			ContentType:          data.stable.ContentType,
			BodySHA256:           data.bodyHashHex,
			AcceptedAt:           data.acceptedAt.Format(TimestampLayout),
			IdempotencyExpiresAt: data.idempotencyExpires.Format(TimestampLayout),
			IdempotencyState:     "committed",
			State:                "durable",
			Duplicate:            data.duplicate,
			StatusPath:           statusPath,
		})
		return
	}

	// Ephemeral backend: emit an explicit, unmistakable evaluation marker.
	header.Set("Hookship-Durability", "ephemeral")
	header.Set("Hookship-Evaluation", "true")
	w.WriteHeader(statusCode)
	_ = json.NewEncoder(w).Encode(evaluationReceipt{
		SchemaVersion:        "1",
		IngestID:             data.ingestID,
		EventID:              data.stable.EventID,
		EventType:            data.stable.EventType,
		Route:                data.stable.Route,
		ContentType:          data.stable.ContentType,
		BodySHA256:           data.bodyHashHex,
		AcceptedAt:           data.acceptedAt.Format(TimestampLayout),
		IdempotencyExpiresAt: data.idempotencyExpires.Format(TimestampLayout),
		IdempotencyState:     "committed",
		State:                "ephemeral",
		Durability:           "ephemeral",
		Evaluation:           true,
		Duplicate:            data.duplicate,
		StatusPath:           statusPath,
		Warning:              "Evaluation backend: accepted events are process-local and lost on restart. Not protocol-v1 durable.",
	})
}

type durableStatus struct {
	SchemaVersion        string `json:"schemaVersion"`
	IngestID             string `json:"ingestId"`
	EventID              string `json:"eventId"`
	EventType            string `json:"eventType"`
	Route                string `json:"route"`
	ContentType          string `json:"contentType"`
	BodySHA256           string `json:"bodySha256"`
	AcceptedAt           string `json:"acceptedAt"`
	IdempotencyExpiresAt string `json:"idempotencyExpiresAt"`
	UpdatedAt            string `json:"updatedAt"`
	IngestState          string `json:"ingestState"`
	IdempotencyState     string `json:"idempotencyState"`
	DispatchState        string `json:"dispatchState"`
}

type evaluationStatus struct {
	SchemaVersion        string `json:"schemaVersion"`
	IngestID             string `json:"ingestId"`
	EventID              string `json:"eventId"`
	EventType            string `json:"eventType"`
	Route                string `json:"route"`
	ContentType          string `json:"contentType"`
	BodySHA256           string `json:"bodySha256"`
	AcceptedAt           string `json:"acceptedAt"`
	IdempotencyExpiresAt string `json:"idempotencyExpiresAt"`
	UpdatedAt            string `json:"updatedAt"`
	IngestState          string `json:"ingestState"`
	IdempotencyState     string `json:"idempotencyState"`
	DispatchState        string `json:"dispatchState"`
	Durability           string `json:"durability"`
	Evaluation           bool   `json:"evaluation"`
}

func (h *Handler) writeStatus(w http.ResponseWriter, r *http.Request, requestID string, status runtime.IngestStatus) {
	header := w.Header()
	header.Set("Content-Type", "application/json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Content-Type-Options", "nosniff")
	header.Set("Hookship-Request-Id", requestID)

	if h.backend.Durable() {
		w.WriteHeader(http.StatusOK)
		_ = json.NewEncoder(w).Encode(durableStatus{
			SchemaVersion:        "1",
			IngestID:             status.IngestID,
			EventID:              status.Headers.EventID,
			EventType:            status.Headers.EventType,
			Route:                status.Headers.Route,
			ContentType:          status.Headers.ContentType,
			BodySHA256:           status.BodySHA256.String(),
			AcceptedAt:           status.AcceptedAt.UTC().Format(TimestampLayout),
			IdempotencyExpiresAt: status.IdempotencyExpiresAt.UTC().Format(TimestampLayout),
			UpdatedAt:            status.UpdatedAt.UTC().Format(TimestampLayout),
			IngestState:          "durable",
			IdempotencyState:     "committed",
			DispatchState:        status.DispatchState,
		})
		return
	}

	header.Set("Hookship-Durability", "ephemeral")
	header.Set("Hookship-Evaluation", "true")
	w.WriteHeader(http.StatusOK)
	_ = json.NewEncoder(w).Encode(evaluationStatus{
		SchemaVersion:        "1",
		IngestID:             status.IngestID,
		EventID:              status.Headers.EventID,
		EventType:            status.Headers.EventType,
		Route:                status.Headers.Route,
		ContentType:          status.Headers.ContentType,
		BodySHA256:           status.BodySHA256.String(),
		AcceptedAt:           status.AcceptedAt.UTC().Format(TimestampLayout),
		IdempotencyExpiresAt: status.IdempotencyExpiresAt.UTC().Format(TimestampLayout),
		UpdatedAt:            status.UpdatedAt.UTC().Format(TimestampLayout),
		IngestState:          "ephemeral",
		IdempotencyState:     "committed",
		DispatchState:        status.DispatchState,
		Durability:           "ephemeral",
		Evaluation:           true,
	})
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}
