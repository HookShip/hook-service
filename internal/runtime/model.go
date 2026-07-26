package runtime

import (
	"time"

	"github.com/hookship/hook-service/internal/idempotency"
)

// Environment classifies an assignment. Evaluation assignments may authenticate
// only against loopback ephemeral profiles and are never durable.
type Environment string

const (
	// EnvironmentProduction marks a durable, non-evaluation assignment.
	EnvironmentProduction Environment = "production"
	// EnvironmentEvaluation marks an ephemeral loopback-only assignment.
	EnvironmentEvaluation Environment = "evaluation"
)

// Assignment is the immutable authorization and delivery scope resolved from a
// verified credential. It is the single source of tenant, environment, route,
// limit, and endpoint truth; producers never supply these.
type Assignment struct {
	ID           string
	CredentialID string
	// HMACSecret authenticates inbound protocol-v1 requests. It never leaves the
	// process and is never logged.
	HMACSecret   []byte
	Environment  Environment
	Routes       []string
	MaxBodyBytes int64
	RetryHorizon time.Duration
	Endpoint     EndpointConfig
	// EndpointSnapshotID is the immutable snapshot the acceptance and delivery
	// records reference. Backends resolve it during registration.
	EndpointSnapshotID string
}

// AllowsRoute reports whether the assignment authorizes a logical route.
func (a Assignment) AllowsRoute(route string) bool {
	for _, allowed := range a.Routes {
		if allowed == route {
			return true
		}
	}
	return false
}

// EndpointConfig is the delivery destination and outbound signing material bound
// to an assignment. SigningSecret is the Standard Webhooks symmetric secret.
type EndpointConfig struct {
	EndpointID       string
	BundleID         string
	ConfigGeneration int64
	URL              string
	// SigningSecret signs outbound Standard Webhooks deliveries. It is scoped to
	// this immutable endpoint configuration and never logged.
	SigningSecret []byte
	MaxAttempts   int
	Timeout       time.Duration
}

// StableHeaders are the signed producer identity headers bound to the
// idempotency fingerprint. Replay-only fields are intentionally excluded. It is
// an alias for idempotency.Headers so the canonical fingerprint identity is
// defined in exactly one place and shared across every profile.
type StableHeaders = idempotency.Headers

// ReservationRequest reserves an idempotency identity before the acceptance
// barrier is crossed.
type ReservationRequest struct {
	LedgerID       string
	AssignmentID   string
	IdempotencyKey string
	BodySHA256     Hash
	Headers        StableHeaders
	ReservedAt     time.Time
}

// ReservationStatus is the durable outcome of an idempotency reservation.
type ReservationStatus string

const (
	ReservationPending          ReservationStatus = "pending"
	ReservationDuplicate        ReservationStatus = "duplicate"
	ReservationConflict         ReservationStatus = "conflict"
	ReservationMetadataConflict ReservationStatus = "metadata_conflict"
	ReservationExpired          ReservationStatus = "expired"
)

// Reservation is the durable idempotency ledger row.
type Reservation struct {
	LedgerID             string
	AssignmentID         string
	IdempotencyKey       string
	BodySHA256           Hash
	Headers              StableHeaders
	State                string
	IngestID             string
	PayloadID            string
	DispatchJobID        string
	ReservedAt           time.Time
	AcceptedAt           *time.Time
	IdempotencyExpiresAt *time.Time
}

// ReservationResult is returned by ReserveIdempotency.
type ReservationResult struct {
	Status      ReservationStatus
	Created     bool
	Reservation Reservation
}

// SealedPayload is the encrypted producer body plus the cryptographic metadata
// required to reproduce and later delete the exact ciphertext.
type SealedPayload struct {
	Ciphertext       []byte
	Nonce            []byte
	KeyReference     string
	KeyVersion       string
	CiphertextSHA256 Hash
}

// AcceptanceRequest crosses the protocol-v1 durability acknowledgement barrier:
// immutable ingest, ledger commit, exact payload commit, and ready dispatch job
// all become durable atomically or not at all.
type AcceptanceRequest struct {
	Assignment          Assignment
	LedgerID            string
	IdempotencyKey      string
	IngestID            string
	PayloadID           string
	DeliveryID          string
	DispatchJobID       string
	EndpointSnapshotID  string
	Payload             SealedPayload
	BodySHA256          Hash
	Headers             StableHeaders
	AcceptedAt          time.Time
	IdempotencyExpires  time.Time
	PayloadExpiresAt    time.Time
	DispatchRunAt       time.Time
	DispatchMaxAttempts int
}

// IngestStatus is the payload-free status view returned by GET status. It never
// contains, reconstructs, or references the raw payload.
type IngestStatus struct {
	IngestID             string
	AssignmentID         string
	Headers              StableHeaders
	BodySHA256           Hash
	AcceptedAt           time.Time
	IdempotencyExpiresAt time.Time
	UpdatedAt            time.Time
	IngestState          string
	IdempotencyState     string
	DispatchState        string
}

// Job is a durable unit of dispatch work claimed under a fencing lease.
type Job struct {
	JobID              string
	AssignmentID       string
	Kind               string
	IngestID           string
	PayloadID          string
	DeliveryID         string
	EndpointSnapshotID string
	State              string
	RunAt              time.Time
	LeaseExpiresAt     *time.Time
	Attempts           int
	MaxAttempts        int
	LastErrorCode      string
}

// JobLease fences a claim; only the owner holding the exact token and attempt
// may settle the job.
type JobLease struct {
	AssignmentID string
	JobID        string
	Owner        string
	Token        string
	Attempt      int
}

// DeliveryContext is everything the worker needs to attempt one delivery,
// loaded from committed durable state. The plaintext body is decrypted only
// inside the worker operation.
type DeliveryContext struct {
	Job          Job
	Endpoint     EndpointConfig
	EventID      string
	EventType    string
	ContentType  string
	Body         []byte
	BodySHA256   Hash
	AssignmentID string
}

// AttemptOutcome classifies a settled delivery attempt.
type AttemptOutcome string

const (
	OutcomeSuccess  AttemptOutcome = "success"
	OutcomeRetry    AttemptOutcome = "retry"
	OutcomeTerminal AttemptOutcome = "terminal"
	OutcomeUnknown  AttemptOutcome = "unknown"
)

// AttemptRecord is an immutable, payload-free record of one delivery attempt.
type AttemptRecord struct {
	AttemptID          string
	AssignmentID       string
	DeliveryID         string
	JobID              string
	EndpointSnapshotID string
	AttemptNumber      int
	Outcome            AttemptOutcome
	ResponseStatus     *int
	ErrorCode          string
	DurationMS         int
	StartedAt          time.Time
	FinishedAt         time.Time
}

// OutboxObservation is a canonical, payload-free delivery observation queued for
// the metadata sink.
type OutboxObservation struct {
	OutboxID           string
	AssignmentID       string
	DeliveryID         string
	AttemptID          string
	EndpointSnapshotID string
	SchemaVersion      string
	EventKind          string
	Outcome            AttemptOutcome
	ResponseStatus     *int
	ErrorCode          string
	OccurredAt         time.Time
	RunAt              time.Time
	MaxAttempts        int
}

// OutboxItem is a claimed metadata outbox row awaiting publication.
type OutboxItem struct {
	OutboxObservation
	State          string
	LeaseExpiresAt *time.Time
	Attempts       int
	LastErrorCode  string
}

// OutboxLease fences an outbox claim.
type OutboxLease struct {
	AssignmentID string
	OutboxID     string
	Owner        string
	Token        string
	Attempt      int
}

// EndpointSnapshot is an immutable endpoint/config snapshot referenced by
// acceptance and delivery records.
type EndpointSnapshot struct {
	SnapshotID       string
	AssignmentID     string
	EndpointID       string
	BundleID         string
	ConfigGeneration int64
	Content          []byte
	ContentSHA256    Hash
	CreatedAt        time.Time
}
