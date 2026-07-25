package config

import (
	"fmt"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/payloadcrypto"
	"os"
	"strings"
	"time"
)

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
