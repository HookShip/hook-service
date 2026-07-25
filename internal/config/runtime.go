package config

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/payloadcrypto"
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

// validateTLSPair rejects a partial TLS configuration: the certificate and key
// must be configured together or not at all.
func (r RuntimeConfig) validateTLSPair() error {
	certSet := strings.TrimSpace(r.Transport.TLSCertFile) != ""
	keySet := strings.TrimSpace(r.Transport.TLSKeyFile) != ""
	if certSet != keySet {
		return fmt.Errorf("%s and %s must be configured together", envTLSCert, envTLSKey)
	}
	return nil
}

func runtimeFromEnv(getenv func(string) string) (RuntimeConfig, error) {
	runtime := RuntimeConfig{
		Backend:        strings.TrimSpace(getenv(envBackend)),
		DatabaseURL:    strings.TrimSpace(getenv(envDatabaseURL)),
		PayloadBackend: strings.TrimSpace(getenv(envPayloadBackend)),
		RedisURL:       strings.TrimSpace(getenv(envRedisURL)),
		ConfigFile:     strings.TrimSpace(getenv(envConfigFile)),
		KeyProvider:    strings.TrimSpace(getenv(envKeyProvider)),
		Worker: WorkerSettings{
			Concurrency:     defaultWorkerConcurrency,
			Lease:           defaultWorkerLease,
			DeliveryTimeout: defaultDeliveryTimeout,
			MaxAttempts:     defaultMaxAttempts,
		},
		Retention: RetentionSettings{
			Interval:        defaultRetentionInterval,
			Batch:           defaultRetentionBatch,
			OutboxRetention: defaultOutboxRetention,
		},
		Sink: SinkSettings{
			OutboxSink:        strings.TrimSpace(getenv(envOutboxSink)),
			SinkURL:           strings.TrimSpace(getenv(envSinkURL)),
			MetadataSinkURL:   strings.TrimSpace(getenv(envMetadataSinkURL)),
			MetadataSinkToken: strings.TrimSpace(getenv(envMetadataSinkToken)),
		},
		Transport: TransportSettings{
			TLSCertFile: strings.TrimSpace(getenv(envTLSCert)),
			TLSKeyFile:  strings.TrimSpace(getenv(envTLSKey)),
		},
		ObjectStore: ObjectStoreSettings{
			Endpoint:        strings.TrimSpace(getenv(envObjectEndpoint)),
			Region:          strings.TrimSpace(getenv(envObjectRegion)),
			Bucket:          strings.TrimSpace(getenv(envObjectBucket)),
			CABundlePath:    strings.TrimSpace(getenv(envObjectCABundle)),
			AccessKeyID:     strings.TrimSpace(getenv(envObjectAccessKey)),
			SecretAccessKey: strings.TrimSpace(getenv(envObjectSecretKey)),
			SessionToken:    strings.TrimSpace(getenv(envObjectSession)),
		},
		KMS: KMSSettings{
			KeyID:           strings.TrimSpace(getenv(envKMSKeyID)),
			Region:          strings.TrimSpace(getenv(envKMSRegion)),
			Endpoint:        strings.TrimSpace(getenv(envKMSEndpoint)),
			AccessKeyID:     strings.TrimSpace(getenv(envKMSAccessKey)),
			SecretAccessKey: strings.TrimSpace(getenv(envKMSSecretKey)),
			SessionToken:    strings.TrimSpace(getenv(envKMSSession)),
			WrappedDataKey:  strings.TrimSpace(getenv(envKMSDataKeyB64)),
		},
	}
	if brokers := strings.TrimSpace(getenv(envKafkaBrokers)); brokers != "" {
		for _, broker := range strings.Split(brokers, ",") {
			if trimmed := strings.TrimSpace(broker); trimmed != "" {
				runtime.KafkaBrokers = append(runtime.KafkaBrokers, trimmed)
			}
		}
	}

	// Typed env descriptors. Each parses one HOOKSHIP_* variable into its typed
	// destination, leaving the default in place when the variable is unset. This
	// replaces ~20 near-identical parse-and-check blocks without reflection.
	parsers := []envParser{
		boolEnv(envEvaluationMode, &runtime.EvaluationMode),
		boolEnv(envProxyMode, &runtime.Transport.ProxyMode),
		boolEnv(envDeliverLoopback, &runtime.Transport.AllowLoopback),
		boolEnv(envDeliverPrivate, &runtime.Transport.AllowPrivate),
		boolEnv(envDeliverPlaintext, &runtime.Transport.AllowPlaintext),

		intEnv(envWorkerConcurrency, &runtime.Worker.Concurrency),
		intEnv(envMaxAttempts, &runtime.Worker.MaxAttempts),
		durationEnv(envWorkerLease, &runtime.Worker.Lease),
		durationEnv(envDeliveryTimeout, &runtime.Worker.DeliveryTimeout),

		intEnv(envEndpointConcurrency, &runtime.RateLimit.EndpointConcurrency),
		intEnv(envAssignmentConcurrency, &runtime.RateLimit.AssignmentConcurrency),
		intEnv(envEndpointBurst, &runtime.RateLimit.EndpointRateBurst),
		intEnv(envAssignmentBurst, &runtime.RateLimit.AssignmentRateBurst),
		floatEnv(envEndpointRate, &runtime.RateLimit.EndpointRatePerSec),
		floatEnv(envAssignmentRate, &runtime.RateLimit.AssignmentRatePerSec),

		intEnv(envRetentionBatch, &runtime.Retention.Batch),
		durationEnv(envRetentionInterval, &runtime.Retention.Interval),
		durationEnv(envOutboxRetention, &runtime.Retention.OutboxRetention),

		boolEnv(envObjectPathStyle, &runtime.ObjectStore.PathStyle),
		boolEnv(envObjectUseTLS, &runtime.ObjectStore.UseTLS),
		boolEnv(envObjectPlaintext, &runtime.ObjectStore.AllowPlaintext),
	}
	for _, parse := range parsers {
		if err := parse(getenv); err != nil {
			return RuntimeConfig{}, err
		}
	}

	if value := strings.TrimSpace(getenv(envPayloadKey)); value != "" {
		key, err := decodeKey(value)
		if err != nil {
			return RuntimeConfig{}, err
		}
		runtime.payloadKey = key
	}
	return runtime, nil
}

// LoadRuntime parses the runtime configuration from the environment without
// running capability validation. It is used by operator tooling that wants to
// inspect or validate a selection explicitly.
func LoadRuntime(getenv func(string) string) (RuntimeConfig, error) {
	return runtimeFromEnv(getenv)
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

// SelectionForListen validates the TLS certificate/key pair and returns the
// capability selection for a listen address. It rejects a partial TLS pair and
// derives the plaintext flag only from a complete pair, so operator tooling
// (doctor, backend validate) and startup validation agree exactly and never
// treat a cert-only configuration as secure.
func (r RuntimeConfig) SelectionForListen(listenAddress string) (capabilities.Selection, error) {
	if err := r.validateTLSPair(); err != nil {
		return capabilities.Selection{}, err
	}
	loopbackOnly := isLoopbackBind(listenAddress)
	plaintext := !r.HasTLS() && !r.Transport.ProxyMode
	return r.Selection(loopbackOnly, plaintext), nil
}

// validateWithSelection checks the runtime selection (operational bounds, the
// backend capability schema, and durable requirements) against a pre-computed
// capability selection.
func (r RuntimeConfig) validateWithSelection(selection capabilities.Selection) error {
	if !r.Selected() {
		return nil
	}
	if r.Worker.Concurrency < 1 || r.Worker.Concurrency > 256 {
		return fmt.Errorf("%s must be between 1 and 256", envWorkerConcurrency)
	}
	if r.Worker.Lease < time.Second || r.Worker.Lease > time.Hour {
		return fmt.Errorf("%s must be between 1s and 1h", envWorkerLease)
	}
	if r.Worker.DeliveryTimeout < time.Second || r.Worker.DeliveryTimeout > 10*time.Minute {
		return fmt.Errorf("%s must be between 1s and 10m", envDeliveryTimeout)
	}
	if r.Worker.MaxAttempts < 1 || r.Worker.MaxAttempts > 1000 {
		return fmt.Errorf("%s must be between 1 and 1000", envMaxAttempts)
	}
	if r.RateLimit.EndpointConcurrency < 0 || r.RateLimit.EndpointConcurrency > 100000 {
		return fmt.Errorf("%s must be between 0 and 100000", envEndpointConcurrency)
	}
	if r.RateLimit.AssignmentConcurrency < 0 || r.RateLimit.AssignmentConcurrency > 100000 {
		return fmt.Errorf("%s must be between 0 and 100000", envAssignmentConcurrency)
	}
	if r.RateLimit.EndpointRateBurst < 0 || r.RateLimit.AssignmentRateBurst < 0 {
		return fmt.Errorf("rate burst values must not be negative")
	}
	if r.Retention.Interval != 0 && (r.Retention.Interval < time.Second || r.Retention.Interval > time.Hour) {
		return fmt.Errorf("%s must be between 1s and 1h", envRetentionInterval)
	}
	if r.Retention.Batch != 0 && (r.Retention.Batch < 1 || r.Retention.Batch > 10000) {
		return fmt.Errorf("%s must be between 1 and 10000", envRetentionBatch)
	}
	if r.Retention.OutboxRetention != 0 && r.Retention.OutboxRetention < time.Minute {
		return fmt.Errorf("%s must be at least 1m", envOutboxRetention)
	}

	result := capabilities.Validate(selection)
	if !result.OK {
		reasons := make([]string, 0, len(result.Errors))
		for _, finding := range result.Errors {
			reasons = append(reasons, fmt.Sprintf("%s (%s)", finding.Reason, finding.ReasonCode))
		}
		return fmt.Errorf("backend selection %q is invalid: %s", r.Backend, strings.Join(reasons, "; "))
	}

	document, _ := capabilities.Lookup(r.Backend)
	durable := document.Properties.Durability != capabilities.DurabilityEphemeral
	if durable && r.DatabaseURL == "" {
		return fmt.Errorf("%s requires %s", r.Backend, envDatabaseURL)
	}
	if len(r.payloadKey) != 0 && len(r.payloadKey) != payloadcrypto.KeySize {
		return fmt.Errorf("%s must decode to %d bytes", envPayloadKey, payloadcrypto.KeySize)
	}
	if err := r.validateKeyProvider(durable); err != nil {
		return err
	}
	if err := r.validateObjectStore(durable); err != nil {
		return err
	}
	return nil
}

// validateKeyProvider enforces the payload key-management selection. Durable
// profiles require either a static local key or the AWS KMS provider so a
// payload key always exists before startup.
func (r RuntimeConfig) validateKeyProvider(durableProfile bool) error {
	switch r.KeyProvider {
	case "", keyProviderStatic:
		if durableProfile && len(r.payloadKey) == 0 {
			return fmt.Errorf("%s requires %s (or %s=%s) for durable payload encryption",
				r.Backend, envPayloadKey, envKeyProvider, keyProviderKMS)
		}
	case keyProviderKMS:
		if strings.TrimSpace(r.KMS.KeyID) == "" {
			return fmt.Errorf("%s=%s requires %s", envKeyProvider, keyProviderKMS, envKMSKeyID)
		}
		if strings.TrimSpace(r.KMS.Region) == "" {
			return fmt.Errorf("%s=%s requires %s", envKeyProvider, keyProviderKMS, envKMSRegion)
		}
		// Credentials may be supplied explicitly here; a KMS endpoint override
		// (used by local fakes) requires them so production never falls back to
		// ambient credentials silently.
		if strings.TrimSpace(r.KMS.Endpoint) != "" &&
			(strings.TrimSpace(r.KMS.AccessKeyID) == "" || strings.TrimSpace(r.KMS.SecretAccessKey) == "") {
			return fmt.Errorf("%s with %s set requires %s and %s",
				keyProviderKMS, envKMSEndpoint, envKMSAccessKey, envKMSSecretKey)
		}
	default:
		return fmt.Errorf("%s must be %q or %q", envKeyProvider, keyProviderStatic, keyProviderKMS)
	}
	return nil
}

// validateObjectStore fails closed on an incomplete or unsafe object-store
// payload configuration. It requires a durable profile and complete settings,
// forbids non-loopback plaintext object transport in production, and verifies
// any supplied CA bundle is readable.
func (r RuntimeConfig) validateObjectStore(durableProfile bool) error {
	if !r.UsesObjectStore() {
		return nil
	}
	if !durableProfile {
		return fmt.Errorf("%s=%s requires a durable backend profile", envPayloadBackend, payloadBackendObjectStore)
	}
	if !r.ObjectStore.Configured() {
		return fmt.Errorf("%s=%s requires %s, %s, %s, %s, and %s",
			envPayloadBackend, payloadBackendObjectStore,
			envObjectEndpoint, envObjectRegion, envObjectBucket, envObjectAccessKey, envObjectSecretKey)
	}
	if !r.ObjectStore.UseTLS && r.Production() && !isLoopbackEndpoint(r.ObjectStore.Endpoint) && !r.ObjectStore.AllowPlaintext {
		return fmt.Errorf("%s must use TLS (%s=true) for a non-loopback endpoint in production, or assert a trusted network with %s=true",
			payloadBackendObjectStore, envObjectUseTLS, envObjectPlaintext)
	}
	if path := strings.TrimSpace(r.ObjectStore.CABundlePath); path != "" {
		if _, err := os.Stat(path); err != nil {
			return fmt.Errorf("%s is not readable: %w", envObjectCABundle, err)
		}
	}
	return nil
}

// UsesObjectStore reports whether the object-store payload backend is selected.
func (r RuntimeConfig) UsesObjectStore() bool {
	return r.PayloadBackend == payloadBackendObjectStore
}

// UsesKMS reports whether the AWS KMS key provider is selected.
func (r RuntimeConfig) UsesKMS() bool {
	return r.KeyProvider == keyProviderKMS
}
