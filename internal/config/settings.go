package config

import (
	"github.com/hookship/hook-service/internal/capabilities"
	"strings"
	"time"
)

const (
	envBackend           = "HOOKSHIP_BACKEND"
	envEvaluationMode    = "HOOKSHIP_EVALUATION_MODE"
	envDatabaseURL       = "HOOKSHIP_DATABASE_URL"
	envPayloadBackend    = "HOOKSHIP_PAYLOAD_BACKEND"
	envPayloadKey        = "HOOKSHIP_PAYLOAD_KEY"
	envRedisURL          = "HOOKSHIP_REDIS_URL"
	envKafkaBrokers      = "HOOKSHIP_KAFKA_BROKERS"
	envConfigFile        = "HOOKSHIP_CONFIG_FILE"
	envWorkerConcurrency = "HOOKSHIP_WORKER_CONCURRENCY"
	envWorkerLease       = "HOOKSHIP_WORKER_LEASE"
	envDeliveryTimeout   = "HOOKSHIP_DELIVERY_TIMEOUT"
	envMaxAttempts       = "HOOKSHIP_MAX_ATTEMPTS"

	// Per-assignment and per-endpoint concurrency and rate limits with bounded
	// backpressure. Zero disables an individual limit.
	envEndpointConcurrency   = "HOOKSHIP_ENDPOINT_MAX_CONCURRENCY"
	envAssignmentConcurrency = "HOOKSHIP_ASSIGNMENT_MAX_CONCURRENCY"
	envEndpointRate          = "HOOKSHIP_ENDPOINT_RATE_PER_SEC"
	envEndpointBurst         = "HOOKSHIP_ENDPOINT_RATE_BURST"
	envAssignmentRate        = "HOOKSHIP_ASSIGNMENT_RATE_PER_SEC"
	envAssignmentBurst       = "HOOKSHIP_ASSIGNMENT_RATE_BURST"

	// Retention / cleanup worker bounds.
	envRetentionInterval = "HOOKSHIP_RETENTION_INTERVAL"
	envRetentionBatch    = "HOOKSHIP_RETENTION_BATCH"
	envOutboxRetention   = "HOOKSHIP_OUTBOX_RETENTION"
	envOutboxSink        = "HOOKSHIP_OUTBOX_SINK"
	envSinkURL           = "HOOKSHIP_SINK_URL"
	envMetadataSinkURL   = "HOOKSHIP_METADATA_SINK_URL"
	envMetadataSinkToken = "HOOKSHIP_METADATA_SINK_TOKEN"
	envProxyMode         = "HOOKSHIP_PROXY_MODE"
	envTLSCert           = "HOOKSHIP_TLS_CERT"
	envTLSKey            = "HOOKSHIP_TLS_KEY"
	envDeliverLoopback   = "HOOKSHIP_DELIVERY_ALLOW_LOOPBACK"
	envDeliverPrivate    = "HOOKSHIP_DELIVERY_ALLOW_PRIVATE"
	envDeliverPlaintext  = "HOOKSHIP_DELIVERY_ALLOW_PLAINTEXT"

	// Object-store payload backend configuration. These are required (and
	// validated to fail closed) only when HOOKSHIP_PAYLOAD_BACKEND=object-store.
	envObjectEndpoint  = "HOOKSHIP_PAYLOAD_OBJECT_ENDPOINT"
	envObjectRegion    = "HOOKSHIP_PAYLOAD_OBJECT_REGION"
	envObjectBucket    = "HOOKSHIP_PAYLOAD_OBJECT_BUCKET"
	envObjectPathStyle = "HOOKSHIP_PAYLOAD_OBJECT_PATH_STYLE"
	envObjectUseTLS    = "HOOKSHIP_PAYLOAD_OBJECT_USE_TLS"
	envObjectCABundle  = "HOOKSHIP_PAYLOAD_OBJECT_CA_BUNDLE"
	envObjectAccessKey = "HOOKSHIP_PAYLOAD_OBJECT_ACCESS_KEY_ID"
	envObjectSecretKey = "HOOKSHIP_PAYLOAD_OBJECT_SECRET_ACCESS_KEY"
	envObjectSession   = "HOOKSHIP_PAYLOAD_OBJECT_SESSION_TOKEN"
	envObjectPlaintext = "HOOKSHIP_PAYLOAD_OBJECT_ALLOW_PLAINTEXT"

	// Object-store key management. The default is a static local key; an
	// aws-kms provider path is credential-gated and validated below.
	envKeyProvider   = "HOOKSHIP_KEY_PROVIDER"
	envKMSKeyID      = "HOOKSHIP_KMS_KEY_ID"
	envKMSRegion     = "HOOKSHIP_KMS_REGION"
	envKMSEndpoint   = "HOOKSHIP_KMS_ENDPOINT"
	envKMSAccessKey  = "HOOKSHIP_KMS_ACCESS_KEY_ID"
	envKMSSecretKey  = "HOOKSHIP_KMS_SECRET_ACCESS_KEY"
	envKMSSession    = "HOOKSHIP_KMS_SESSION_TOKEN"
	envKMSDataKeyB64 = "HOOKSHIP_KMS_WRAPPED_DATA_KEY"

	payloadBackendObjectStore = "object-store"
	keyProviderStatic         = "static"
	keyProviderKMS            = "aws-kms"

	defaultWorkerConcurrency = 4
	defaultWorkerLease       = 30 * time.Second
	defaultDeliveryTimeout   = 30 * time.Second
	defaultMaxAttempts       = 12
	defaultRetentionInterval = time.Minute
	defaultRetentionBatch    = 500
	defaultOutboxRetention   = 24 * time.Hour
)

// RuntimeConfig captures the selected native runtime profile and its bounded
// operational settings, grouped into cohesive sections. It is empty
// (Backend == "") until an operator selects a backend, preserving the
// health-only process for existing deployments.
type RuntimeConfig struct {
	Backend        string
	EvaluationMode bool
	DatabaseURL    string
	PayloadBackend string
	RedisURL       string
	KafkaBrokers   []string
	ConfigFile     string

	// Worker holds the delivery worker's concurrency and timing bounds.
	Worker WorkerSettings
	// RateLimit holds per-assignment/per-endpoint concurrency and rate limits.
	RateLimit RateLimitSettings
	// Retention holds the retention/cleanup worker bounds.
	Retention RetentionSettings
	// Sink holds the metadata outbox sink selection and destinations.
	Sink SinkSettings
	// Transport holds listener/TLS and delivery SSRF-allowance settings.
	Transport TransportSettings

	// ObjectStore holds the S3-compatible payload store settings. It is used and
	// validated only when PayloadBackend == "object-store".
	ObjectStore ObjectStoreSettings
	// KeyProvider selects payload key management ("static" default, or
	// "aws-kms"). KMS holds the AWS KMS provider settings.
	KeyProvider string
	KMS         KMSSettings

	payloadKey []byte
}

// WorkerSettings bounds the delivery worker.
type WorkerSettings struct {
	Concurrency     int
	Lease           time.Duration
	DeliveryTimeout time.Duration
	MaxAttempts     int
}

// RateLimitSettings holds per-assignment and per-endpoint concurrency and rate
// limits with bounded backpressure. Zero disables an individual limit.
type RateLimitSettings struct {
	EndpointConcurrency   int
	AssignmentConcurrency int
	EndpointRatePerSec    float64
	EndpointRateBurst     int
	AssignmentRatePerSec  float64
	AssignmentRateBurst   int
}

// RetentionSettings bounds the retention/cleanup worker.
type RetentionSettings struct {
	Interval        time.Duration
	Batch           int
	OutboxRetention time.Duration
}

// SinkSettings selects and locates the payload-free metadata outbox sink.
type SinkSettings struct {
	OutboxSink        string
	SinkURL           string
	MetadataSinkURL   string
	MetadataSinkToken string
}

// TransportSettings holds listener/TLS and delivery SSRF-allowance settings. The
// delivery allowances all default false (SSRF-safe); they are explicit opt-ins
// for trusted local/VM sinks and reduce SSRF protection when enabled.
type TransportSettings struct {
	ProxyMode      bool
	TLSCertFile    string
	TLSKeyFile     string
	AllowLoopback  bool
	AllowPrivate   bool
	AllowPlaintext bool
}

// ObjectStoreSettings is the validated S3-compatible object-store configuration.
// Credentials are never logged.
type ObjectStoreSettings struct {
	Endpoint        string
	Region          string
	Bucket          string
	PathStyle       bool
	UseTLS          bool
	AllowPlaintext  bool
	CABundlePath    string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
}

// Configured reports whether the minimum required object-store fields are set.
func (o ObjectStoreSettings) Configured() bool {
	return o.Endpoint != "" && o.Region != "" && o.Bucket != "" &&
		o.AccessKeyID != "" && o.SecretAccessKey != ""
}

// KMSSettings is the AWS-compatible KMS key provider configuration. Credentials
// are never logged.
type KMSSettings struct {
	KeyID           string
	Region          string
	Endpoint        string
	AccessKeyID     string
	SecretAccessKey string
	SessionToken    string
	WrappedDataKey  string
}

// Selected reports whether a native runtime profile was configured.
func (r RuntimeConfig) Selected() bool { return strings.TrimSpace(r.Backend) != "" }

// PayloadKey returns the configured 32-byte payload key, or nil when none was
// supplied (the evaluation backend generates an ephemeral key in that case).
func (r RuntimeConfig) PayloadKey() []byte { return r.payloadKey }

// Production reports whether the selection is treated as a production/managed
// assignment (anything other than explicit evaluation mode).
func (r RuntimeConfig) Production() bool { return !r.EvaluationMode }

// AllowLoopbackDelivery reports whether the delivery transport may reach
// loopback destinations. Evaluation mode or an explicit opt-in enables it.
func (r RuntimeConfig) AllowLoopbackDelivery() bool {
	return r.EvaluationMode || r.Transport.AllowLoopback
}

// AllowPrivateDelivery reports whether the delivery transport may reach private
// (RFC 1918 / ULA) destinations. This is an explicit opt-in for trusted local
// networks and container sinks.
func (r RuntimeConfig) AllowPrivateDelivery() bool {
	return r.Transport.AllowPrivate
}

// AllowPlaintextDelivery reports whether the delivery transport may use plaintext
// http. Evaluation mode or an explicit opt-in enables it.
func (r RuntimeConfig) AllowPlaintextDelivery() bool {
	return r.EvaluationMode || r.Transport.AllowPlaintext
}

// HasTLS reports whether a complete TLS certificate/key pair is configured for
// direct HTTPS termination by the process itself.
func (r RuntimeConfig) HasTLS() bool {
	return r.Transport.TLSCertFile != "" && r.Transport.TLSKeyFile != ""
}

// Selection builds a capability selection from the runtime configuration using
// the supplied process bind properties.
func (r RuntimeConfig) Selection(loopbackOnly, plaintext bool) capabilities.Selection {
	return capabilities.Selection{
		Profile:         r.Backend,
		EvaluationMode:  r.EvaluationMode,
		Production:      r.Production(),
		LoopbackOnly:    loopbackOnly,
		PlaintextListen: plaintext,
		RedisConfigured: r.RedisURL != "",
		KafkaConfigured: len(r.KafkaBrokers) > 0,
		PayloadBackend:  capabilities.PayloadBackend(r.PayloadBackend),
	}
}

// UsesObjectStore reports whether the object-store payload backend is selected.
func (r RuntimeConfig) UsesObjectStore() bool {
	return r.PayloadBackend == payloadBackendObjectStore
}

// UsesKMS reports whether the AWS KMS key provider is selected.
func (r RuntimeConfig) UsesKMS() bool {
	return r.KeyProvider == keyProviderKMS
}
