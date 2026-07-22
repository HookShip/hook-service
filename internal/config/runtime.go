package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"
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
// operational settings. It is empty (Backend == "") until an operator selects a
// backend, preserving the health-only process for existing deployments.
type RuntimeConfig struct {
	Backend           string
	EvaluationMode    bool
	DatabaseURL       string
	PayloadBackend    string
	RedisURL          string
	KafkaBrokers      []string
	ConfigFile        string
	WorkerConcurrency int
	WorkerLease       time.Duration
	DeliveryTimeout   time.Duration
	MaxAttempts       int
	OutboxSink        string
	SinkURL           string
	MetadataSinkURL   string
	MetadataSinkToken string
	ProxyMode         bool
	TLSCertFile       string
	TLSKeyFile        string

	// Delivery transport SSRF allowances. All default false (SSRF-safe). They
	// are explicit opt-ins for delivering to trusted local/VM sinks and reduce
	// SSRF protection when enabled.
	DeliveryAllowLoopback  bool
	DeliveryAllowPrivate   bool
	DeliveryAllowPlaintext bool

	// ObjectStore holds the S3-compatible payload store settings. It is used and
	// validated only when PayloadBackend == "object-store".
	ObjectStore ObjectStoreSettings
	// KeyProvider selects payload key management ("static" default, or
	// "aws-kms"). KMS holds the AWS KMS provider settings.
	KeyProvider string
	KMS         KMSSettings

	// Delivery concurrency and rate limits with bounded backpressure. Zero
	// disables an individual limit.
	EndpointConcurrency   int
	AssignmentConcurrency int
	EndpointRatePerSec    float64
	EndpointRateBurst     int
	AssignmentRatePerSec  float64
	AssignmentRateBurst   int

	// Retention worker bounds.
	RetentionInterval time.Duration
	RetentionBatch    int
	OutboxRetention   time.Duration

	payloadKey []byte
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
	return r.EvaluationMode || r.DeliveryAllowLoopback
}

// AllowPrivateDelivery reports whether the delivery transport may reach private
// (RFC 1918 / ULA) destinations. This is an explicit opt-in for trusted local
// networks and container sinks.
func (r RuntimeConfig) AllowPrivateDelivery() bool {
	return r.DeliveryAllowPrivate
}

// AllowPlaintextDelivery reports whether the delivery transport may use plaintext
// http. Evaluation mode or an explicit opt-in enables it.
func (r RuntimeConfig) AllowPlaintextDelivery() bool {
	return r.EvaluationMode || r.DeliveryAllowPlaintext
}

// HasTLS reports whether a complete TLS certificate/key pair is configured for
// direct HTTPS termination by the process itself.
func (r RuntimeConfig) HasTLS() bool {
	return r.TLSCertFile != "" && r.TLSKeyFile != ""
}

// validateTLSPair rejects a partial TLS configuration: the certificate and key
// must be configured together or not at all.
func (r RuntimeConfig) validateTLSPair() error {
	certSet := strings.TrimSpace(r.TLSCertFile) != ""
	keySet := strings.TrimSpace(r.TLSKeyFile) != ""
	if certSet != keySet {
		return fmt.Errorf("%s and %s must be configured together", envTLSCert, envTLSKey)
	}
	return nil
}

func runtimeFromEnv(getenv func(string) string) (RuntimeConfig, error) {
	runtime := RuntimeConfig{
		Backend:           strings.TrimSpace(getenv(envBackend)),
		DatabaseURL:       strings.TrimSpace(getenv(envDatabaseURL)),
		PayloadBackend:    strings.TrimSpace(getenv(envPayloadBackend)),
		RedisURL:          strings.TrimSpace(getenv(envRedisURL)),
		ConfigFile:        strings.TrimSpace(getenv(envConfigFile)),
		WorkerConcurrency: defaultWorkerConcurrency,
		WorkerLease:       defaultWorkerLease,
		DeliveryTimeout:   defaultDeliveryTimeout,
		MaxAttempts:       defaultMaxAttempts,
		RetentionInterval: defaultRetentionInterval,
		RetentionBatch:    defaultRetentionBatch,
		OutboxRetention:   defaultOutboxRetention,
		OutboxSink:        strings.TrimSpace(getenv(envOutboxSink)),
		SinkURL:           strings.TrimSpace(getenv(envSinkURL)),
		MetadataSinkURL:   strings.TrimSpace(getenv(envMetadataSinkURL)),
		MetadataSinkToken: strings.TrimSpace(getenv(envMetadataSinkToken)),
		TLSCertFile:       strings.TrimSpace(getenv(envTLSCert)),
		TLSKeyFile:        strings.TrimSpace(getenv(envTLSKey)),
	}
	if brokers := strings.TrimSpace(getenv(envKafkaBrokers)); brokers != "" {
		for _, broker := range strings.Split(brokers, ",") {
			if trimmed := strings.TrimSpace(broker); trimmed != "" {
				runtime.KafkaBrokers = append(runtime.KafkaBrokers, trimmed)
			}
		}
	}

	var err error
	if runtime.EvaluationMode, err = boolFromEnv(getenv, envEvaluationMode); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.ProxyMode, err = boolFromEnv(getenv, envProxyMode); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.DeliveryAllowLoopback, err = boolFromEnv(getenv, envDeliverLoopback); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.DeliveryAllowPrivate, err = boolFromEnv(getenv, envDeliverPrivate); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.DeliveryAllowPlaintext, err = boolFromEnv(getenv, envDeliverPlaintext); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envWorkerConcurrency, &runtime.WorkerConcurrency); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envMaxAttempts, &runtime.MaxAttempts); err != nil {
		return RuntimeConfig{}, err
	}
	if err := durationFromEnv(getenv, envWorkerLease, &runtime.WorkerLease); err != nil {
		return RuntimeConfig{}, err
	}
	if err := durationFromEnv(getenv, envDeliveryTimeout, &runtime.DeliveryTimeout); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envEndpointConcurrency, &runtime.EndpointConcurrency); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envAssignmentConcurrency, &runtime.AssignmentConcurrency); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envEndpointBurst, &runtime.EndpointRateBurst); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envAssignmentBurst, &runtime.AssignmentRateBurst); err != nil {
		return RuntimeConfig{}, err
	}
	if err := intFromEnv(getenv, envRetentionBatch, &runtime.RetentionBatch); err != nil {
		return RuntimeConfig{}, err
	}
	if err := floatFromEnv(getenv, envEndpointRate, &runtime.EndpointRatePerSec); err != nil {
		return RuntimeConfig{}, err
	}
	if err := floatFromEnv(getenv, envAssignmentRate, &runtime.AssignmentRatePerSec); err != nil {
		return RuntimeConfig{}, err
	}
	if err := durationFromEnv(getenv, envRetentionInterval, &runtime.RetentionInterval); err != nil {
		return RuntimeConfig{}, err
	}
	if err := durationFromEnv(getenv, envOutboxRetention, &runtime.OutboxRetention); err != nil {
		return RuntimeConfig{}, err
	}
	if value := strings.TrimSpace(getenv(envPayloadKey)); value != "" {
		key, err := decodeKey(value)
		if err != nil {
			return RuntimeConfig{}, err
		}
		runtime.payloadKey = key
	}

	runtime.ObjectStore = ObjectStoreSettings{
		Endpoint:        strings.TrimSpace(getenv(envObjectEndpoint)),
		Region:          strings.TrimSpace(getenv(envObjectRegion)),
		Bucket:          strings.TrimSpace(getenv(envObjectBucket)),
		CABundlePath:    strings.TrimSpace(getenv(envObjectCABundle)),
		AccessKeyID:     strings.TrimSpace(getenv(envObjectAccessKey)),
		SecretAccessKey: strings.TrimSpace(getenv(envObjectSecretKey)),
		SessionToken:    strings.TrimSpace(getenv(envObjectSession)),
	}
	if runtime.ObjectStore.PathStyle, err = boolFromEnv(getenv, envObjectPathStyle); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.ObjectStore.UseTLS, err = boolFromEnv(getenv, envObjectUseTLS); err != nil {
		return RuntimeConfig{}, err
	}
	if runtime.ObjectStore.AllowPlaintext, err = boolFromEnv(getenv, envObjectPlaintext); err != nil {
		return RuntimeConfig{}, err
	}

	runtime.KeyProvider = strings.TrimSpace(getenv(envKeyProvider))
	runtime.KMS = KMSSettings{
		KeyID:           strings.TrimSpace(getenv(envKMSKeyID)),
		Region:          strings.TrimSpace(getenv(envKMSRegion)),
		Endpoint:        strings.TrimSpace(getenv(envKMSEndpoint)),
		AccessKeyID:     strings.TrimSpace(getenv(envKMSAccessKey)),
		SecretAccessKey: strings.TrimSpace(getenv(envKMSSecretKey)),
		SessionToken:    strings.TrimSpace(getenv(envKMSSession)),
		WrappedDataKey:  strings.TrimSpace(getenv(envKMSDataKeyB64)),
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
	plaintext := !r.HasTLS() && !r.ProxyMode
	return r.Selection(loopbackOnly, plaintext), nil
}

// validateWithSelection checks the runtime selection (operational bounds, the
// backend capability schema, and durable requirements) against a pre-computed
// capability selection.
func (r RuntimeConfig) validateWithSelection(selection capabilities.Selection) error {
	if !r.Selected() {
		return nil
	}
	if r.WorkerConcurrency < 1 || r.WorkerConcurrency > 256 {
		return fmt.Errorf("%s must be between 1 and 256", envWorkerConcurrency)
	}
	if r.WorkerLease < time.Second || r.WorkerLease > time.Hour {
		return fmt.Errorf("%s must be between 1s and 1h", envWorkerLease)
	}
	if r.DeliveryTimeout < time.Second || r.DeliveryTimeout > 10*time.Minute {
		return fmt.Errorf("%s must be between 1s and 10m", envDeliveryTimeout)
	}
	if r.MaxAttempts < 1 || r.MaxAttempts > 1000 {
		return fmt.Errorf("%s must be between 1 and 1000", envMaxAttempts)
	}
	if r.EndpointConcurrency < 0 || r.EndpointConcurrency > 100000 {
		return fmt.Errorf("%s must be between 0 and 100000", envEndpointConcurrency)
	}
	if r.AssignmentConcurrency < 0 || r.AssignmentConcurrency > 100000 {
		return fmt.Errorf("%s must be between 0 and 100000", envAssignmentConcurrency)
	}
	if r.EndpointRateBurst < 0 || r.AssignmentRateBurst < 0 {
		return fmt.Errorf("rate burst values must not be negative")
	}
	if r.RetentionInterval != 0 && (r.RetentionInterval < time.Second || r.RetentionInterval > time.Hour) {
		return fmt.Errorf("%s must be between 1s and 1h", envRetentionInterval)
	}
	if r.RetentionBatch != 0 && (r.RetentionBatch < 1 || r.RetentionBatch > 10000) {
		return fmt.Errorf("%s must be between 1 and 10000", envRetentionBatch)
	}
	if r.OutboxRetention != 0 && r.OutboxRetention < time.Minute {
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

func decodeKey(value string) ([]byte, error) {
	if decoded, err := hex.DecodeString(value); err == nil && len(decoded) == payloadcrypto.KeySize {
		return decoded, nil
	}
	if decoded, err := base64.StdEncoding.DecodeString(value); err == nil && len(decoded) == payloadcrypto.KeySize {
		return decoded, nil
	}
	return nil, fmt.Errorf("%s must be a %d-byte key in hex or base64", envPayloadKey, payloadcrypto.KeySize)
}

func boolFromEnv(getenv func(string) string, name string) (bool, error) {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return false, nil
	}
	parsed, err := strconv.ParseBool(value)
	if err != nil {
		return false, fmt.Errorf("%s must be a boolean: %w", name, err)
	}
	return parsed, nil
}

func intFromEnv(getenv func(string) string, name string, destination *int) error {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.Atoi(value)
	if err != nil {
		return fmt.Errorf("%s must be an integer: %w", name, err)
	}
	*destination = parsed
	return nil
}

func floatFromEnv(getenv func(string) string, name string, destination *float64) error {
	value := strings.TrimSpace(getenv(name))
	if value == "" {
		return nil
	}
	parsed, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return fmt.Errorf("%s must be a number: %w", name, err)
	}
	if parsed < 0 {
		return fmt.Errorf("%s must not be negative", name)
	}
	*destination = parsed
	return nil
}

// isLoopbackBind reports whether a listen address binds only the loopback
// interface.
func isLoopbackBind(address string) bool {
	host, _, err := net.SplitHostPort(address)
	if err != nil {
		return false
	}
	host = strings.TrimSpace(host)
	switch host {
	case "localhost":
		return true
	case "":
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// isLoopbackEndpoint reports whether an object-store endpoint URL or host:port
// targets the loopback interface, so local MinIO over plaintext HTTP is allowed
// while a remote plaintext endpoint fails closed in production.
func isLoopbackEndpoint(endpoint string) bool {
	endpoint = strings.TrimSpace(endpoint)
	if endpoint == "" {
		return false
	}
	host := endpoint
	if strings.Contains(endpoint, "://") {
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return false
		}
		host = parsed.Host
	}
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.TrimSpace(host)
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}
