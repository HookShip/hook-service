// Package capabilities defines the versioned, machine-readable backend
// capability schema owned by hook-service. Every native runtime profile
// declares its durability, consistency, ordering, delivery semantics, and
// operational requirements so an operator sees the tradeoffs and stable reason
// codes before selecting a profile. Invalid selections are rejected before
// startup with a stable reason code and the missing capability.
package capabilities

// SchemaVersion identifies the capability document wire format. It is stable and
// authoritative; incompatible changes require a new version.
const SchemaVersion = "backend_capabilities/v1"

// SchemaID is the local URN of the capability JSON Schema. It is deliberately
// not a resolvable web URL.
const SchemaID = "urn:hookship:backend-capabilities:v1"

// Kind identifies the document type.
const Kind = "backend_capability"

// Status is the maturity of a profile or of an individual capability.
type Status string

const (
	StatusSupported    Status = "supported"
	StatusExperimental Status = "experimental"
	StatusDegraded     Status = "degraded"
	StatusUnsupported  Status = "unsupported"
)

// Durability describes how accepted state survives failure.
type Durability string

const (
	DurabilityEphemeral  Durability = "ephemeral"
	DurabilityDisk       Durability = "disk"
	DurabilityReplicated Durability = "replicated"
)

// Consistency describes the state store's consistency model.
type Consistency string

const (
	ConsistencyProcessLocal Consistency = "process_local"
	ConsistencyStrong       Consistency = "strong"
	ConsistencyEventual     Consistency = "eventual"
)

// Ordering describes the strongest ordering scope a profile provides.
type Ordering string

const (
	OrderingNone       Ordering = "none"
	OrderingEndpoint   Ordering = "endpoint"
	OrderingAssignment Ordering = "assignment"
	OrderingPartition  Ordering = "partition"
)

// DeliverySemantics describes the delivery guarantee. HookShip never claims
// exactly-once.
type DeliverySemantics string

const (
	DeliveryAtLeastOnce DeliverySemantics = "at_least_once"
	DeliveryAtMostOnce  DeliverySemantics = "at_most_once"
)

// DeploymentMode is a supported packaging/placement mode.
type DeploymentMode string

const (
	DeploymentDeveloper DeploymentMode = "developer"
	DeploymentSingleVM  DeploymentMode = "single_vm"
	DeploymentManaged   DeploymentMode = "managed_cell"
)

// ReasonCode is a stable, machine-readable justification for a status or a
// validation finding. Codes are part of the compatibility surface and must not
// be renamed without a schema version bump.
type ReasonCode string

const (
	ReasonNone                   ReasonCode = ""
	ReasonProtocolV1Durable      ReasonCode = "protocol_v1_durable"
	ReasonEphemeralEvaluation    ReasonCode = "ephemeral_evaluation_only"
	ReasonPostgresSourceOfTruth  ReasonCode = "postgres_source_of_truth"
	ReasonOutboxRelay            ReasonCode = "transactional_outbox_relay"
	ReasonExperimentalObjectPort ReasonCode = "object_store_port_incomplete"
	ReasonUnsafeDualWrite        ReasonCode = "unsafe_dual_write_forbidden"
	ReasonNativeLedgerUnbuilt    ReasonCode = "native_queue_ledger_not_designed"

	// Validation-only reason codes.
	ReasonUnknownProfile      ReasonCode = "unknown_profile"
	ReasonEvaluationRequired  ReasonCode = "evaluation_mode_required"
	ReasonProductionForbidden ReasonCode = "production_selection_forbidden"
	ReasonMissingService      ReasonCode = "required_external_service_missing"
	ReasonPlaintextExposed    ReasonCode = "non_loopback_plaintext_forbidden"
	ReasonExactlyOnce         ReasonCode = "exactly_once_unsupported"
	ReasonExperimentalInProd  ReasonCode = "experimental_backend_in_production"
	ReasonUnsupportedPayload  ReasonCode = "unsupported_payload_backend"
)

// Bounds describes the maximum tested operating envelope. Values outside these
// bounds are untested, not necessarily broken. Every field is always present.
type Bounds struct {
	MaxPayloadBytes        int64 `json:"maxPayloadBytes"`
	MaxAssignments         int   `json:"maxAssignments"`
	MaxInFlightJobs        int   `json:"maxInFlightJobs"`
	TestedThroughputPerSec int   `json:"testedThroughputPerSec"`
}

// Capability is one declared property with a status and reason. Every field is
// always present so the wire shape is deterministic.
type Capability struct {
	Supported  bool       `json:"supported"`
	Status     Status     `json:"status"`
	ReasonCode ReasonCode `json:"reasonCode"`
	Reason     string     `json:"reason"`
}

// Properties groups the descriptive and behavioral scalar properties.
type Properties struct {
	DisplayName           string            `json:"displayName"`
	Summary               string            `json:"summary"`
	EvaluationOnly        bool              `json:"evaluationOnly"`
	Status                Status            `json:"status"`
	ReasonCode            ReasonCode        `json:"reasonCode"`
	Reason                string            `json:"reason"`
	StateBackend          string            `json:"stateBackend"`
	DispatchQueue         string            `json:"dispatchQueue"`
	PayloadBackend        string            `json:"payloadBackend"`
	Durability            Durability        `json:"durability"`
	Consistency           Consistency       `json:"consistency"`
	DeliverySemantics     DeliverySemantics `json:"deliverySemantics"`
	Ordering              Ordering          `json:"ordering"`
	OrderingNote          string            `json:"orderingNote"`
	RetentionHorizon      string            `json:"retentionHorizon"`
	DelayedRetryStrategy  string            `json:"delayedRetryStrategy"`
	FailureRecovery       string            `json:"failureRecovery"`
	OperationalComplexity string            `json:"operationalComplexity"`
	TypicalUse            string            `json:"typicalUse"`
}

// CapabilitySet groups the per-capability declarations.
type CapabilitySet struct {
	AcknowledgementBarrier Capability `json:"acknowledgementBarrier"`
	DurableProtocolV1      Capability `json:"durableProtocolV1Acknowledgement"`
	RestartRecovery        Capability `json:"restartRecovery"`
	DurableIdempotency     Capability `json:"durableIdempotency"`
	LeaseFencing           Capability `json:"leaseFencing"`
	MultiReplica           Capability `json:"multiReplicaSafe"`
	MultiProcess           Capability `json:"multiProcessSafe"`
	ReplayRedrive          Capability `json:"replayRedrive"`
}

// Requirements groups the operational requirements.
type Requirements struct {
	BackupRequirement string           `json:"backupRequirement"`
	ExternalServices  []string         `json:"requiredExternalServices"`
	DeploymentModes   []DeploymentMode `json:"deploymentModes"`
}

// Document is the full capability declaration for one profile. It is
// machine-readable and stable across a schema version. Its JSON shape is the
// authoritative cross-repo contract emitted by `hookshipd capabilities` and
// `hookshipctl backend describe` and mirrored by the fixtures under
// spec/backend-capabilities/v1.
type Document struct {
	Schema        string        `json:"$schema"`
	SchemaVersion string        `json:"schemaVersion"`
	Kind          string        `json:"kind"`
	Profile       string        `json:"profile"`
	Properties    Properties    `json:"properties"`
	Capabilities  CapabilitySet `json:"capabilities"`
	Bounds        Bounds        `json:"bounds"`
	Requirements  Requirements  `json:"requirements"`
}
