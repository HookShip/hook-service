package capabilities

import "fmt"

// PayloadBackend identifies the configured payload store for a selection.
type PayloadBackend string

const (
	PayloadMemory      PayloadBackend = "memory"
	PayloadPostgres    PayloadBackend = "postgres"
	PayloadObjectStore PayloadBackend = "object-store"
)

// Selection is the operator's configured choice, validated against the profile
// capability document before startup.
type Selection struct {
	Profile            string
	EvaluationMode     bool
	Production         bool
	LoopbackOnly       bool
	PlaintextListen    bool
	RedisConfigured    bool
	KafkaConfigured    bool
	PayloadBackend     PayloadBackend
	RequireExactlyOnce bool
}

// Finding is one validation outcome bound to a stable reason code.
type Finding struct {
	ReasonCode ReasonCode `json:"reasonCode"`
	Capability string     `json:"capability"`
	Reason     string     `json:"reason"`
}

// ValidationResult is the machine-readable outcome of validating a selection.
type ValidationResult struct {
	Profile  string    `json:"profile"`
	OK       bool      `json:"ok"`
	Errors   []Finding `json:"errors,omitempty"`
	Warnings []Finding `json:"warnings,omitempty"`
}

// Validate checks a selection against the profile capability document, returning
// hard errors for invalid combinations and warnings for degraded/experimental
// choices. It never hides a tradeoff or silently permits an unsafe combination.
func Validate(selection Selection) ValidationResult {
	result := ValidationResult{Profile: selection.Profile}

	document, ok := Lookup(selection.Profile)
	if !ok {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonUnknownProfile,
			Capability: "profile",
			Reason:     fmt.Sprintf("unknown profile %q; known profiles: %v", selection.Profile, Names()),
		})
		return finalize(result)
	}

	if document.Properties.Status == StatusUnsupported {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: document.Properties.ReasonCode,
			Capability: "profile",
			Reason:     document.Properties.Reason,
		})
	}

	if selection.RequireExactlyOnce {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonExactlyOnce,
			Capability: "deliverySemantics",
			Reason:     "exactly-once delivery is never provided; only at-least-once is supported.",
		})
	}

	// Ephemeral profiles are evaluation-only and never durable.
	if document.Properties.Durability == DurabilityEphemeral {
		if !selection.EvaluationMode {
			result.Errors = append(result.Errors, Finding{
				ReasonCode: ReasonEvaluationRequired,
				Capability: "durability",
				Reason:     "the memory profile requires evaluation mode; it cannot accept durable protocol-v1 events.",
			})
		}
		if selection.Production {
			result.Errors = append(result.Errors, Finding{
				ReasonCode: ReasonProductionForbidden,
				Capability: "durability",
				Reason:     "the memory profile must never be selected for a production or managed assignment.",
			})
		}
	}

	// Required external services must be configured.
	if selection.Profile == ProfilePostgresRedis && !selection.RedisConfigured {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonMissingService,
			Capability: "dispatchQueue",
			Reason:     "postgres-redis requires a configured Redis endpoint (HOOKSHIP_REDIS_URL).",
		})
	}
	if selection.Profile == ProfilePostgresKafka && !selection.KafkaConfigured {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonMissingService,
			Capability: "dispatchQueue",
			Reason:     "postgres-kafka requires configured Kafka brokers (HOOKSHIP_KAFKA_BROKERS).",
		})
	}

	// Non-loopback plaintext must fail closed regardless of profile.
	if selection.PlaintextListen && !selection.LoopbackOnly {
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonPlaintextExposed,
			Capability: "transport",
			Reason:     "a non-loopback plaintext listener is forbidden; use TLS or explicit proxy mode.",
		})
	}

	// Payload backend validation.
	switch selection.PayloadBackend {
	case "", PayloadMemory, PayloadPostgres:
		// Supported payload backends.
	case PayloadObjectStore:
		// The S3-compatible object-store payload backend is wired: ciphertext is
		// written to an immutable object store and PostgreSQL keeps only the
		// payload-free locator, checksum, and key metadata. It requires a durable
		// profile; the object-store connection settings are validated separately
		// by the configuration layer, which fails closed on unsafe production
		// settings.
		if document.Properties.Durability == DurabilityEphemeral {
			result.Errors = append(result.Errors, Finding{
				ReasonCode: ReasonObjectStoreDurable,
				Capability: "payloadBackend",
				Reason:     "the object-store payload backend requires a durable profile; the evaluation profile keeps ciphertext in memory.",
			})
		}
	default:
		result.Errors = append(result.Errors, Finding{
			ReasonCode: ReasonUnsupportedPayload,
			Capability: "payloadBackend",
			Reason:     fmt.Sprintf("unknown payload backend %q", selection.PayloadBackend),
		})
	}

	// Surface degraded/experimental profile status as a warning.
	if document.Properties.Status == StatusExperimental || document.Properties.Status == StatusDegraded {
		result.Warnings = append(result.Warnings, Finding{
			ReasonCode: document.Properties.ReasonCode,
			Capability: "profile",
			Reason:     document.Properties.Reason,
		})
	}

	return finalize(result)
}

func finalize(result ValidationResult) ValidationResult {
	result.OK = len(result.Errors) == 0
	return result
}
