package capabilities

import (
	"sort"
	"time"
)

// Profile identifiers for the built-in native runtime profiles.
const (
	ProfileMemory        = "memory"
	ProfilePostgres      = "postgres"
	ProfilePostgresRedis = "postgres-redis"
	ProfilePostgresKafka = "postgres-kafka"

	// ProfileKafkaNativeLedger is a reserved future profile that would make
	// Kafka (not PostgreSQL) the protocol-v1 acceptance ledger. It is
	// intentionally declared unsupported with a reason until a separate
	// protocol/storage design exists.
	ProfileKafkaNativeLedger = "kafka-native-ledger"
)

const retentionHorizon = 24 * time.Hour

func supported(reason ReasonCode, text string) Capability {
	return Capability{Supported: true, Status: StatusSupported, ReasonCode: reason, Reason: text}
}

func unsupported(reason ReasonCode, text string) Capability {
	return Capability{Supported: false, Status: StatusUnsupported, ReasonCode: reason, Reason: text}
}

// Profiles returns every built-in profile document in a stable order.
func Profiles() []Document {
	documents := []Document{
		memoryDocument(),
		postgresDocument(),
		postgresRedisDocument(),
		postgresKafkaDocument(),
		kafkaNativeLedgerDocument(),
	}
	sort.SliceStable(documents, func(i, j int) bool {
		return rank(documents[i].Profile) < rank(documents[j].Profile)
	})
	return documents
}

func rank(profile string) int {
	switch profile {
	case ProfileMemory:
		return 0
	case ProfilePostgres:
		return 1
	case ProfilePostgresRedis:
		return 2
	case ProfilePostgresKafka:
		return 3
	default:
		return 9
	}
}

// Lookup returns the capability document for a profile.
func Lookup(profile string) (Document, bool) {
	for _, document := range Profiles() {
		if document.Profile == profile {
			return document, true
		}
	}
	return Document{}, false
}

// Names returns the selectable profile names in stable order.
func Names() []string {
	documents := Profiles()
	names := make([]string, 0, len(documents))
	for _, document := range documents {
		names = append(names, document.Profile)
	}
	return names
}

func baseDocument(profile, displayName string) Document {
	return Document{
		Schema:        SchemaID,
		SchemaVersion: SchemaVersion,
		Kind:          Kind,
		Profile:       profile,
		Properties:    Properties{DisplayName: displayName},
		Requirements: Requirements{
			ExternalServices: []string{},
			DeploymentModes:  []DeploymentMode{},
		},
	}
}

func memoryDocument() Document {
	document := baseDocument(ProfileMemory, "Memory")
	document.Properties = Properties{
		DisplayName:           "Memory",
		Summary:               "Process-local ephemeral backend for tests, demos, and evaluation.",
		EvaluationOnly:        true,
		Status:                StatusSupported,
		ReasonCode:            ReasonEphemeralEvaluation,
		Reason:                "Fully functional for evaluation but loses accepted events on restart; never durable.",
		StateBackend:          "memory",
		DispatchQueue:         "memory",
		PayloadBackend:        "memory",
		Durability:            DurabilityEphemeral,
		Consistency:           ConsistencyProcessLocal,
		DeliverySemantics:     DeliveryAtLeastOnce,
		Ordering:              OrderingNone,
		OrderingNote:          "Process-local timer scheduling; no cross-endpoint ordering guarantee.",
		RetentionHorizon:      "process lifetime (bounded TTL)",
		DelayedRetryStrategy:  "in-process timer",
		FailureRecovery:       "none; a restart is a clean slate by design.",
		OperationalComplexity: "minimal",
		TypicalUse:            "tests and demos",
	}
	document.Capabilities = CapabilitySet{
		AcknowledgementBarrier: unsupported(ReasonEphemeralEvaluation, "Acceptance is in-memory only; a restart loses accepted events."),
		DurableProtocolV1:      unsupported(ReasonEphemeralEvaluation, "Ephemeral state cannot provide protocol-v1 durable acknowledgement."),
		RestartRecovery:        unsupported(ReasonEphemeralEvaluation, "All state is process-local and disappears on restart."),
		DurableIdempotency:     unsupported(ReasonEphemeralEvaluation, "Idempotency ledger is bounded and process-local."),
		LeaseFencing:           supported(ReasonNone, "In-process fencing tokens prevent stale settlement within one process."),
		MultiReplica:           unsupported(ReasonEphemeralEvaluation, "State is not shared across processes or replicas."),
		MultiProcess:           unsupported(ReasonEphemeralEvaluation, "State is not shared across processes."),
		ReplayRedrive:          supported(ReasonNone, "Dead jobs can be redriven within the running process."),
	}
	document.Bounds = Bounds{MaxPayloadBytes: 1 << 20, MaxAssignments: 256, MaxInFlightJobs: 10000}
	document.Requirements = Requirements{
		BackupRequirement: "none (ephemeral)",
		ExternalServices:  []string{},
		DeploymentModes:   []DeploymentMode{DeploymentDeveloper},
	}
	return document
}

func postgresDocument() Document {
	document := baseDocument(ProfilePostgres, "PostgreSQL")
	document.Properties = Properties{
		DisplayName:           "PostgreSQL",
		Summary:               "Simplest durable profile: PostgreSQL is state, queue, and payload store.",
		EvaluationOnly:        false,
		Status:                StatusSupported,
		ReasonCode:            ReasonProtocolV1Durable,
		Reason:                "PostgreSQL is the source of truth and the protocol-v1 acceptance barrier.",
		StateBackend:          "postgres",
		DispatchQueue:         "postgres leases",
		PayloadBackend:        "postgres ciphertext",
		Durability:            DurabilityDisk,
		Consistency:           ConsistencyStrong,
		DeliverySemantics:     DeliveryAtLeastOnce,
		Ordering:              OrderingEndpoint,
		OrderingNote:          "run_at ordering per endpoint; no strict global ordering.",
		RetentionHorizon:      retentionHorizon.String(),
		DelayedRetryStrategy:  "native run_at query",
		FailureRecovery:       "crash before commit returns retryable 503; no false acceptance.",
		OperationalComplexity: "low",
		TypicalUse:            "local and single-VM durable delivery",
	}
	document.Capabilities = CapabilitySet{
		AcknowledgementBarrier: supported(ReasonProtocolV1Durable, "Ingest, ledger, payload, and dispatch job commit in one transaction."),
		DurableProtocolV1:      supported(ReasonProtocolV1Durable, "202 is returned only after the four artifacts are durable."),
		RestartRecovery:        supported(ReasonProtocolV1Durable, "Committed state and pending reservations recover after restart."),
		DurableIdempotency:     supported(ReasonProtocolV1Durable, "Idempotency ledger is durable for the full retry horizon."),
		LeaseFencing:           supported(ReasonNone, "Database-clock leases with fencing tokens prevent stale settlement."),
		MultiReplica:           supported(ReasonNone, "SKIP LOCKED claims allow multiple API/worker replicas."),
		MultiProcess:           supported(ReasonNone, "Multiple worker processes claim safely."),
		ReplayRedrive:          supported(ReasonNone, "Dead jobs are surfaced and redriven by explicit operator action."),
	}
	document.Bounds = Bounds{MaxPayloadBytes: 16 << 20, MaxInFlightJobs: 100000}
	document.Requirements = Requirements{
		BackupRequirement: "PostgreSQL physical or logical backup of the cell database.",
		ExternalServices:  []string{"postgresql>=17"},
		DeploymentModes:   []DeploymentMode{DeploymentDeveloper, DeploymentSingleVM, DeploymentManaged},
	}
	return document
}

func postgresRedisDocument() Document {
	document := postgresDocument()
	document.Profile = ProfilePostgresRedis
	document.Properties.DisplayName = "PostgreSQL + Redis"
	document.Properties.Summary = "PostgreSQL source of truth with a Redis Streams dispatch relay for lower latency."
	document.Properties.ReasonCode = ReasonOutboxRelay
	document.Properties.Reason = "PostgreSQL remains the acceptance barrier; Redis Streams only carry dispatch notifications derived from a transactional outbox."
	document.Properties.DispatchQueue = "redis streams (from transactional outbox)"
	document.Properties.Durability = DurabilityReplicated
	document.Properties.OrderingNote = "PostgreSQL claim ordering preserved; Redis stream is a wake-up notification, not the ordering authority."
	document.Properties.DelayedRetryStrategy = "native run_at query with Redis wake-up"
	document.Properties.FailureRecovery = "loss of Redis is recoverable by replaying unacknowledged outbox rows; no dual write."
	document.Properties.OperationalComplexity = "medium"
	document.Properties.TypicalUse = "small/medium low-latency delivery"
	document.Capabilities.RestartRecovery = supported(ReasonOutboxRelay, "PostgreSQL recovers fully; the Redis stream is rebuilt by replaying unpublished outbox rows.")
	document.Requirements.ExternalServices = []string{"postgresql>=17", "redis>=7"}
	return document
}

func postgresKafkaDocument() Document {
	document := postgresDocument()
	document.Profile = ProfilePostgresKafka
	document.Properties.DisplayName = "PostgreSQL + Kafka"
	document.Properties.Summary = "PostgreSQL source of truth with a Kafka dispatch relay for high-throughput cells."
	document.Properties.ReasonCode = ReasonOutboxRelay
	document.Properties.Reason = "PostgreSQL remains the acceptance barrier; Kafka carries partition-scoped dispatch work derived from a transactional outbox."
	document.Properties.DispatchQueue = "kafka (from transactional outbox)"
	document.Properties.Durability = DurabilityReplicated
	document.Properties.Ordering = OrderingPartition
	document.Properties.OrderingNote = "Kafka partition-scoped ordering keyed by assignment/endpoint; PostgreSQL is the acceptance authority."
	document.Properties.DelayedRetryStrategy = "native run_at query with Kafka relay; retry topics/scheduler emulate delay"
	document.Properties.FailureRecovery = "loss of Kafka is recoverable by replaying unacknowledged outbox rows; no dual write."
	document.Properties.OperationalComplexity = "high"
	document.Properties.TypicalUse = "high-throughput production cells"
	document.Capabilities.RestartRecovery = supported(ReasonOutboxRelay, "PostgreSQL recovers fully; Kafka and the outbox reconcile by replaying unacknowledged rows.")
	document.Requirements.ExternalServices = []string{"postgresql>=17", "kafka>=3.5 (or api-compatible broker)"}
	return document
}

// kafkaNativeLedgerDocument declares an explicitly unsupported future profile so
// the schema can advertise the boundary honestly.
func kafkaNativeLedgerDocument() Document {
	document := baseDocument(ProfileKafkaNativeLedger, "Kafka native ledger (reserved)")
	document.Properties = Properties{
		DisplayName:           "Kafka native ledger (reserved)",
		Summary:               "Reserved: Kafka as the protocol-v1 acceptance ledger. Not designed yet.",
		EvaluationOnly:        false,
		Status:                StatusUnsupported,
		ReasonCode:            ReasonNativeLedgerUnbuilt,
		Reason:                "A Kafka-native acceptance ledger requires a separate protocol and storage design; PostgreSQL remains the source of truth in this release.",
		StateBackend:          "kafka (proposed)",
		DispatchQueue:         "kafka",
		PayloadBackend:        "unspecified",
		Durability:            DurabilityReplicated,
		Consistency:           ConsistencyEventual,
		DeliverySemantics:     DeliveryAtLeastOnce,
		Ordering:              OrderingPartition,
		OrderingNote:          "",
		RetentionHorizon:      "undefined",
		DelayedRetryStrategy:  "undefined",
		FailureRecovery:       "undefined",
		OperationalComplexity: "undefined",
		TypicalUse:            "not available",
	}
	document.Capabilities = CapabilitySet{
		AcknowledgementBarrier: unsupported(ReasonNativeLedgerUnbuilt, "No acceptance barrier is defined for a Kafka-only ledger."),
		DurableProtocolV1:      unsupported(ReasonNativeLedgerUnbuilt, "Protocol-v1 acceptance is not defined against a Kafka-only ledger."),
		RestartRecovery:        unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
		DurableIdempotency:     unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
		LeaseFencing:           unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
		MultiReplica:           unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
		MultiProcess:           unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
		ReplayRedrive:          unsupported(ReasonNativeLedgerUnbuilt, "Undefined."),
	}
	document.Bounds = Bounds{}
	document.Requirements = Requirements{
		BackupRequirement: "undefined",
		ExternalServices:  []string{"kafka>=3.5"},
		DeploymentModes:   []DeploymentMode{},
	}
	return document
}
