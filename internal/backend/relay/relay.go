// Package relay carries dispatch notifications derived from the durable
// PostgreSQL job set to Redis Streams or Kafka. PostgreSQL remains the source of
// truth and the protocol-v1 acceptance barrier; relayed signals are advisory
// wake-ups that reduce claim latency and, for Kafka, provide partition-scoped
// ordering. Loss of the relay reverts the worker to PostgreSQL polling and no
// work is lost; duplicate signals are safe because claims are idempotent. There
// is no database-plus-queue dual write: signals are produced only after the job
// is committed to PostgreSQL.
package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// SchemaVersion identifies the dispatch signal wire format.
const SchemaVersion = "dispatch_signal/v1"

// Signal is an advisory dispatch notification derived from a committed durable
// job. It carries no payload or secret material.
type Signal struct {
	SchemaVersion      string `json:"schemaVersion"`
	AssignmentID       string `json:"assignmentId"`
	JobID              string `json:"jobId"`
	EndpointSnapshotID string `json:"endpointSnapshotId"`
	RunAtUnixMS        int64  `json:"runAtUnixMs"`
}

// NewSignal builds a signal with the current schema version.
func NewSignal(assignmentID, jobID, endpointSnapshotID string, runAtUnixMS int64) Signal {
	return Signal{
		SchemaVersion:      SchemaVersion,
		AssignmentID:       assignmentID,
		JobID:              jobID,
		EndpointSnapshotID: endpointSnapshotID,
		RunAtUnixMS:        runAtUnixMS,
	}
}

// Encode serializes a signal to bytes.
func Encode(signal Signal) ([]byte, error) {
	if signal.SchemaVersion == "" {
		signal.SchemaVersion = SchemaVersion
	}
	return json.Marshal(signal)
}

// Decode parses a signal, rejecting an unknown schema version.
func Decode(data []byte) (Signal, error) {
	var signal Signal
	if err := json.Unmarshal(data, &signal); err != nil {
		return Signal{}, fmt.Errorf("decode dispatch signal: %w", err)
	}
	if signal.SchemaVersion != SchemaVersion {
		return Signal{}, fmt.Errorf("unsupported dispatch signal schema %q", signal.SchemaVersion)
	}
	if signal.AssignmentID == "" || signal.JobID == "" {
		return Signal{}, errors.New("dispatch signal is missing identifiers")
	}
	return signal, nil
}

// PartitionKey returns the ordering key for a signal. Ordering is scoped to the
// endpoint so that one endpoint's deliveries stay on one partition.
func (s Signal) PartitionKey() string {
	if s.EndpointSnapshotID != "" {
		return s.EndpointSnapshotID
	}
	return s.AssignmentID
}

// Publisher publishes dispatch signals to the relay transport.
type Publisher interface {
	Publish(ctx context.Context, signal Signal) error
	Close() error
}

// Consumer consumes dispatch signals from the relay transport, invoking handle
// for each. It blocks until the context is canceled.
type Consumer interface {
	Consume(ctx context.Context, handle func(context.Context, Signal) error) error
	Close() error
}
