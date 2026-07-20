// Package outboxsink publishes canonical, payload-free delivery observations
// from the metadata outbox to a configured sink: a local file/log sink for
// development and tests, or an authenticated platform-compatible metadata sink.
// Observations never contain payload bytes, secrets, or arbitrary headers.
package outboxsink

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"os"
	"sync"
	"time"
)

// Observation is the canonical, payload-free delivery observation. Its JSON
// shape is the metadata wire contract shared with the platform control plane.
type Observation struct {
	SchemaVersion      string    `json:"schemaVersion"`
	AssignmentID       string    `json:"assignmentId"`
	DeliveryID         string    `json:"deliveryId"`
	AttemptID          string    `json:"attemptId"`
	EndpointSnapshotID string    `json:"endpointSnapshotId"`
	EventKind          string    `json:"eventKind"`
	Outcome            string    `json:"outcome"`
	ResponseStatus     *int      `json:"responseStatus,omitempty"`
	ErrorCode          string    `json:"errorCode,omitempty"`
	OccurredAt         time.Time `json:"occurredAt"`
}

// Sink publishes observations. Implementations are safe for concurrent use.
type Sink interface {
	Publish(ctx context.Context, observation Observation) error
	Name() string
	Close() error
}

// NoopSink discards observations. It is the default when no sink is configured.
type NoopSink struct{}

// Publish discards the observation.
func (NoopSink) Publish(context.Context, Observation) error { return nil }

// Name identifies the sink.
func (NoopSink) Name() string { return "noop" }

// Close is a no-op.
func (NoopSink) Close() error { return nil }

// LocalSink writes newline-delimited JSON observations to a file or writer. It
// backs the developer/test metadata sink.
type LocalSink struct {
	mu     sync.Mutex
	writer io.Writer
	closer io.Closer
	logger *slog.Logger
}

// NewLocalFileSink opens (or creates) a file for appended JSON observations.
func NewLocalFileSink(path string) (*LocalSink, error) {
	if path == "" {
		return nil, errors.New("local sink path is required")
	}
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	return &LocalSink{writer: file, closer: file}, nil
}

// NewLocalLogSink writes observations to the supplied logger at info level.
func NewLocalLogSink(logger *slog.Logger) *LocalSink {
	if logger == nil {
		logger = slog.Default()
	}
	return &LocalSink{logger: logger}
}

// Publish writes one JSON observation.
func (s *LocalSink) Publish(_ context.Context, observation Observation) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.logger != nil {
		s.logger.Info("delivery observation",
			slog.String("assignment_id", observation.AssignmentID),
			slog.String("delivery_id", observation.DeliveryID),
			slog.String("attempt_id", observation.AttemptID),
			slog.String("outcome", observation.Outcome),
			slog.String("error_code", observation.ErrorCode),
		)
		return nil
	}
	encoded, err := json.Marshal(observation)
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	_, err = s.writer.Write(encoded)
	return err
}

// Name identifies the sink.
func (s *LocalSink) Name() string { return "local" }

// Close closes the underlying file if any.
func (s *LocalSink) Close() error {
	if s.closer != nil {
		return s.closer.Close()
	}
	return nil
}
