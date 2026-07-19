package postgres

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"strings"
	"time"
)

const (
	MinimumRetryHorizon     = 24 * time.Hour
	MinimumTombstoneHorizon = 24 * time.Hour
	MaximumPostgresPayload  = 16 << 20
)

var (
	ErrNotPending         = errors.New("idempotency reservation is not pending")
	ErrPayloadConflict    = errors.New("payload identity or cryptographic metadata conflicts")
	ErrPayloadUnavailable = errors.New("payload staging record is no longer recoverable")
	ErrSnapshotConflict   = errors.New("endpoint snapshot identity conflicts")
	ErrLeaseLost          = errors.New("lease was lost or fenced")
	ErrNotFound           = errors.New("durability record not found")
)

type Hash [sha256.Size]byte

func SHA256(value []byte) Hash {
	return sha256.Sum256(value)
}

func ParseHash(value string) (Hash, error) {
	var result Hash
	decoded, err := hex.DecodeString(value)
	if err != nil || len(decoded) != sha256.Size {
		return result, errors.New("hash must be 64 lowercase hexadecimal characters")
	}
	if value != strings.ToLower(value) {
		return result, errors.New("hash must be lowercase hexadecimal")
	}
	copy(result[:], decoded)
	return result, nil
}

func (h Hash) String() string {
	return hex.EncodeToString(h[:])
}

type StableHeaders struct {
	EventID     string
	EventType   string
	Route       string
	ContentType string
}

func (h StableHeaders) validate() error {
	values := []struct {
		name  string
		value string
	}{
		{"event ID", h.EventID},
		{"event type", h.EventType},
		{"route", h.Route},
		{"content type", h.ContentType},
	}
	for _, item := range values {
		if err := validateText(item.name, item.value, 255); err != nil {
			return err
		}
	}
	return nil
}

// RequestFingerprint binds the exact body hash and all stable signed event
// headers. Replay-only timestamp, nonce, signature, and credential ID are
// intentionally excluded.
func RequestFingerprint(bodySHA256 Hash, headers StableHeaders) Hash {
	digest := sha256.New()
	_, _ = digest.Write([]byte("hookship-idempotency-fingerprint-v1\x00"))
	_, _ = digest.Write(bodySHA256[:])
	for _, value := range []string{
		headers.EventID,
		headers.EventType,
		headers.Route,
		headers.ContentType,
	} {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = digest.Write(length[:])
		_, _ = digest.Write([]byte(value))
	}
	var result Hash
	copy(result[:], digest.Sum(nil))
	return result
}

type ReservationRequest struct {
	LedgerID       string
	AssignmentID   string
	IdempotencyKey string
	BodySHA256     Hash
	Headers        StableHeaders
	ReservedAt     time.Time
}

type ReservationStatus string

const (
	ReservationPending          ReservationStatus = "pending"
	ReservationDuplicate        ReservationStatus = "duplicate"
	ReservationConflict         ReservationStatus = "conflict"
	ReservationMetadataConflict ReservationStatus = "metadata_conflict"
	ReservationExpired          ReservationStatus = "expired"
)

type Reservation struct {
	LedgerID             string
	AssignmentID         string
	IdempotencyKey       string
	RequestFingerprint   Hash
	BodySHA256           Hash
	Headers              StableHeaders
	State                string
	IngestID             string
	PayloadID            string
	DispatchJobID        string
	ReservedAt           time.Time
	AcceptedAt           *time.Time
	IdempotencyExpiresAt *time.Time
	TombstoneExpiresAt   *time.Time
}

type ReservationResult struct {
	Status      ReservationStatus
	Created     bool
	Reservation Reservation
}

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

type PostgresAcceptance struct {
	AssignmentID        string
	IdempotencyKey      string
	LedgerID            string
	IngestID            string
	PayloadID           string
	DeliveryID          string
	DispatchJobID       string
	EndpointSnapshotID  string
	Ciphertext          []byte
	KeyReference        string
	KeyVersion          string
	Nonce               []byte
	CiphertextSHA256    Hash
	PayloadExpiresAt    time.Time
	AcceptedAt          time.Time
	IdempotencyExpires  time.Time
	DispatchRunAt       time.Time
	DispatchMaxAttempts int
}

type ObjectPayloadStage struct {
	AssignmentID     string
	LedgerID         string
	PayloadID        string
	KeyReference     string
	KeyVersion       string
	Nonce            []byte
	CiphertextSHA256 Hash
	CiphertextSize   int64
	ExpiresAt        time.Time
	StagedAt         time.Time
}

type ObjectPayloadStageRecord struct {
	AssignmentID     string
	LedgerID         string
	PayloadID        string
	State            string
	KeyReference     string
	KeyVersion       string
	Nonce            []byte
	CiphertextSHA256 Hash
	CiphertextSize   int64
	StageAttemptID   string
	StageAttempt     int
	StagingObjectKey string
	FinalObjectKey   string
	ExpiresAt        time.Time
	StagedAt         time.Time
	FinalizedAt      *time.Time
}

type ObjectStageWriteLease struct {
	AssignmentID     string
	LedgerID         string
	PayloadID        string
	StageAttemptID   string
	Attempt          int
	StagingObjectKey string
	FinalObjectKey   string
	KeyReference     string
	KeyVersion       string
	Nonce            []byte
	CiphertextSHA256 Hash
	CiphertextSize   int64
	Owner            string
	Token            string
	ExpiresAt        time.Time
}

type ObjectPromotion struct {
	ProviderStagingVersion string
	ProviderFinalVersion   string
	Mode                   string
	ExpectedFinalVersion   string
}

type ObjectStageCleanupRecord struct {
	AssignmentID           string
	PayloadID              string
	StageAttemptID         string
	Attempt                int
	State                  string
	StagingObjectKey       string
	FinalObjectKey         string
	ProviderStagingVersion string
	ProviderFinalVersion   string
}

type ObjectAcceptance struct {
	AssignmentID        string
	IdempotencyKey      string
	LedgerID            string
	IngestID            string
	PayloadID           string
	DeliveryID          string
	DispatchJobID       string
	EndpointSnapshotID  string
	AcceptedAt          time.Time
	IdempotencyExpires  time.Time
	DispatchRunAt       time.Time
	DispatchMaxAttempts int
}

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
	LeaseOwner         string
	LeaseToken         string
	LeaseExpiresAt     *time.Time
	Attempts           int
	MaxAttempts        int
	LastErrorCode      string
}

type JobLease struct {
	AssignmentID string
	JobID        string
	Owner        string
	Token        string
	Attempt      int
}

type AttemptRecord struct {
	AttemptID          string
	AssignmentID       string
	DeliveryID         string
	JobID              string
	EndpointSnapshotID string
	AttemptNumber      int
	Outcome            string
	ResponseStatus     *int
	ErrorCode          string
	DurationMS         int
	StartedAt          time.Time
	FinishedAt         time.Time
}

type OutboxRecord struct {
	OutboxID           string
	AssignmentID       string
	DeliveryID         string
	AttemptID          string
	EndpointSnapshotID string
	SchemaVersion      string
	EventKind          string
	Outcome            string
	ResponseStatus     *int
	ErrorCode          string
	OccurredAt         time.Time
	RunAt              time.Time
	MaxAttempts        int
}

type OutboxItem struct {
	OutboxRecord
	State          string
	LeaseOwner     string
	LeaseToken     string
	LeaseExpiresAt *time.Time
	Attempts       int
	LastErrorCode  string
}

type OutboxLease struct {
	AssignmentID string
	OutboxID     string
	Owner        string
	Token        string
	Attempt      int
}

type PayloadDeletionLease struct {
	AssignmentID     string
	PayloadID        string
	Backend          string
	KeyReference     string
	KeyVersion       string
	CiphertextSHA256 Hash
	CiphertextSize   int64
	StagingObjectKey string
	FinalObjectKey   string
	Owner            string
	Token            string
	Attempt          int
	StartedAt        time.Time
	ExpiresAt        time.Time
}

func ObjectKeys(assignmentID, payloadID string) (staging, final string, err error) {
	return ObjectAttemptKeys(assignmentID, payloadID, 1)
}

func ObjectAttemptKeys(
	assignmentID, payloadID string,
	attempt int,
) (staging, final string, err error) {
	if err := validateText("assignment ID", assignmentID, 255); err != nil {
		return "", "", err
	}
	if err := validateText("payload ID", payloadID, 255); err != nil {
		return "", "", err
	}
	if attempt < 1 {
		return "", "", errors.New("object stage attempt must be positive")
	}
	assignmentHash := sha256.Sum256([]byte(assignmentID))
	payloadHash := sha256.Sum256([]byte(payloadID))
	prefix := fmt.Sprintf(
		"hookship/v1/%s/%s",
		hex.EncodeToString(assignmentHash[:]),
		hex.EncodeToString(payloadHash[:]),
	)
	return fmt.Sprintf(
			"%s/attempts/%010d/staging/ciphertext",
			prefix,
			attempt,
		), fmt.Sprintf(
			"%s/attempts/%010d/final/ciphertext",
			prefix,
			attempt,
		), nil
}

func stageWriteAttemptID(assignmentID, payloadID string, attempt int) string {
	sum := sha256.Sum256([]byte(fmt.Sprintf(
		"hookship-stage-write-attempt-v1\x00%s\x00%s\x00%d",
		assignmentID,
		payloadID,
		attempt,
	)))
	return "swa_" + hex.EncodeToString(sum[:16])
}

func validateText(name, value string, maximum int) error {
	if value == "" || value != strings.TrimSpace(value) {
		return fmt.Errorf("%s must be non-empty without outer whitespace", name)
	}
	if len(value) > maximum {
		return fmt.Errorf("%s exceeds %d bytes", name, maximum)
	}
	if strings.ContainsAny(value, "\x00\r\n") {
		return fmt.Errorf("%s contains invalid control characters", name)
	}
	return nil
}

func normalizeTimes(acceptedAt, expiresAt time.Time) (time.Time, time.Time, time.Time, error) {
	if acceptedAt.IsZero() {
		return time.Time{}, time.Time{}, time.Time{}, errors.New("accepted time is required")
	}
	acceptedAt = acceptedAt.UTC().Truncate(time.Microsecond)
	if expiresAt.IsZero() {
		expiresAt = acceptedAt.Add(MinimumRetryHorizon)
	}
	expiresAt = expiresAt.UTC().Truncate(time.Microsecond)
	if expiresAt.Before(acceptedAt.Add(MinimumRetryHorizon)) {
		return time.Time{}, time.Time{}, time.Time{}, errors.New("idempotency expiry must be at least 24 hours after acceptance")
	}
	tombstoneExpiresAt := expiresAt.Add(MinimumTombstoneHorizon)
	return acceptedAt, expiresAt, tombstoneExpiresAt, nil
}
