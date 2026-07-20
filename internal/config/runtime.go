package config

import (
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net"
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

	defaultWorkerConcurrency = 4
	defaultWorkerLease       = 30 * time.Second
	defaultDeliveryTimeout   = 30 * time.Second
	defaultMaxAttempts       = 12
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

	payloadKey []byte
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

	result := capabilities.Validate(selection)
	if !result.OK {
		reasons := make([]string, 0, len(result.Errors))
		for _, finding := range result.Errors {
			reasons = append(reasons, fmt.Sprintf("%s (%s)", finding.Reason, finding.ReasonCode))
		}
		return fmt.Errorf("backend selection %q is invalid: %s", r.Backend, strings.Join(reasons, "; "))
	}

	document, _ := capabilities.Lookup(r.Backend)
	if document.Properties.Durability != capabilities.DurabilityEphemeral && r.DatabaseURL == "" {
		return fmt.Errorf("%s requires %s", r.Backend, envDatabaseURL)
	}
	if len(r.payloadKey) != 0 && len(r.payloadKey) != payloadcrypto.KeySize {
		return fmt.Errorf("%s must decode to %d bytes", envPayloadKey, payloadcrypto.KeySize)
	}
	if document.Properties.Durability != capabilities.DurabilityEphemeral && len(r.payloadKey) == 0 {
		return fmt.Errorf("%s requires %s for durable payload encryption", r.Backend, envPayloadKey)
	}
	return nil
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
