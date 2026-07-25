package config

import (
	"strings"
)

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
