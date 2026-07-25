package config

import (
	"strings"
	"testing"
	"time"
)

// validDurableEnv is a minimal valid durable configuration. Individual tests
// clone it and override one variable to isolate parsing and validation.
func validDurableEnv() map[string]string {
	return map[string]string{
		envBackend:       "postgres",
		envDatabaseURL:   "postgres://localhost/hookship",
		envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		envListenAddress: "127.0.0.1:8080",
	}
}

func withEnv(overrides map[string]string) map[string]string {
	env := validDurableEnv()
	for k, v := range overrides {
		env[k] = v
	}
	return env
}

// TestRuntimeEnvParsesIntoGroups proves each operational variable is parsed into
// its cohesive settings group with the right typed value.
func TestRuntimeEnvParsesIntoGroups(t *testing.T) {
	cfg, err := FromEnv(envMap(withEnv(map[string]string{
		envWorkerConcurrency:     "8",
		envWorkerLease:           "45s",
		envDeliveryTimeout:       "20s",
		envMaxAttempts:           "7",
		envEndpointConcurrency:   "10",
		envAssignmentConcurrency: "20",
		envEndpointRate:          "2.5",
		envEndpointBurst:         "5",
		envAssignmentRate:        "3.5",
		envAssignmentBurst:       "6",
		envRetentionInterval:     "2m",
		envRetentionBatch:        "250",
		envOutboxRetention:       "48h",
		envProxyMode:             "true",
		envDeliverPrivate:        "true",
	})))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	r := cfg.Runtime
	checks := []struct {
		name string
		got  any
		want any
	}{
		{"Worker.Concurrency", r.Worker.Concurrency, 8},
		{"Worker.Lease", r.Worker.Lease, 45 * time.Second},
		{"Worker.DeliveryTimeout", r.Worker.DeliveryTimeout, 20 * time.Second},
		{"Worker.MaxAttempts", r.Worker.MaxAttempts, 7},
		{"RateLimit.EndpointConcurrency", r.RateLimit.EndpointConcurrency, 10},
		{"RateLimit.AssignmentConcurrency", r.RateLimit.AssignmentConcurrency, 20},
		{"RateLimit.EndpointRatePerSec", r.RateLimit.EndpointRatePerSec, 2.5},
		{"RateLimit.EndpointRateBurst", r.RateLimit.EndpointRateBurst, 5},
		{"RateLimit.AssignmentRatePerSec", r.RateLimit.AssignmentRatePerSec, 3.5},
		{"RateLimit.AssignmentRateBurst", r.RateLimit.AssignmentRateBurst, 6},
		{"Retention.Interval", r.Retention.Interval, 2 * time.Minute},
		{"Retention.Batch", r.Retention.Batch, 250},
		{"Retention.OutboxRetention", r.Retention.OutboxRetention, 48 * time.Hour},
		{"Transport.ProxyMode", r.Transport.ProxyMode, true},
		{"Transport.AllowPrivate", r.Transport.AllowPrivate, true},
	}
	for _, c := range checks {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v", c.name, c.got, c.want)
		}
	}
}

// TestRuntimeEnvDefaults proves unset operational variables keep their defaults.
func TestRuntimeEnvDefaults(t *testing.T) {
	cfg, err := FromEnv(envMap(validDurableEnv()))
	if err != nil {
		t.Fatalf("valid config rejected: %v", err)
	}
	r := cfg.Runtime
	if r.Worker.Concurrency != defaultWorkerConcurrency {
		t.Errorf("Worker.Concurrency default = %d, want %d", r.Worker.Concurrency, defaultWorkerConcurrency)
	}
	if r.Worker.Lease != defaultWorkerLease {
		t.Errorf("Worker.Lease default = %v, want %v", r.Worker.Lease, defaultWorkerLease)
	}
	if r.Worker.MaxAttempts != defaultMaxAttempts {
		t.Errorf("Worker.MaxAttempts default = %d, want %d", r.Worker.MaxAttempts, defaultMaxAttempts)
	}
	if r.Retention.Interval != defaultRetentionInterval {
		t.Errorf("Retention.Interval default = %v, want %v", r.Retention.Interval, defaultRetentionInterval)
	}
	if r.Retention.Batch != defaultRetentionBatch {
		t.Errorf("Retention.Batch default = %d, want %d", r.Retention.Batch, defaultRetentionBatch)
	}
}

// TestRuntimeEnvValidationBounds exercises every bounded variable at an invalid
// value and asserts the error names the offending variable.
func TestRuntimeEnvValidationBounds(t *testing.T) {
	cases := []struct {
		name    string
		env     string
		value   string
		wantErr string
	}{
		{"worker concurrency too low", envWorkerConcurrency, "0", envWorkerConcurrency},
		{"worker concurrency too high", envWorkerConcurrency, "257", envWorkerConcurrency},
		{"worker lease too short", envWorkerLease, "500ms", envWorkerLease},
		{"worker lease too long", envWorkerLease, "2h", envWorkerLease},
		{"delivery timeout too short", envDeliveryTimeout, "500ms", envDeliveryTimeout},
		{"delivery timeout too long", envDeliveryTimeout, "11m", envDeliveryTimeout},
		{"max attempts too low", envMaxAttempts, "0", envMaxAttempts},
		{"max attempts too high", envMaxAttempts, "1001", envMaxAttempts},
		{"endpoint concurrency negative", envEndpointConcurrency, "-1", envEndpointConcurrency},
		{"endpoint concurrency too high", envEndpointConcurrency, "100001", envEndpointConcurrency},
		{"assignment concurrency negative", envAssignmentConcurrency, "-1", envAssignmentConcurrency},
		{"retention interval too short", envRetentionInterval, "500ms", envRetentionInterval},
		{"retention interval too long", envRetentionInterval, "2h", envRetentionInterval},
		{"retention batch negative", envRetentionBatch, "-1", envRetentionBatch},
		{"retention batch too high", envRetentionBatch, "10001", envRetentionBatch},
		{"outbox retention too short", envOutboxRetention, "30s", envOutboxRetention},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := FromEnv(envMap(withEnv(map[string]string{c.env: c.value})))
			if err == nil {
				t.Fatalf("expected error for %s=%s", c.env, c.value)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Fatalf("error %q does not mention %s", err.Error(), c.wantErr)
			}
		})
	}
}

// TestRuntimeEnvBoundaryValuesAccepted proves the inclusive bounds are accepted.
func TestRuntimeEnvBoundaryValuesAccepted(t *testing.T) {
	cases := []map[string]string{
		{envWorkerConcurrency: "1"},
		{envWorkerConcurrency: "256"},
		{envWorkerLease: "1s"},
		{envWorkerLease: "1h"},
		{envMaxAttempts: "1000"},
		{envRetentionBatch: "1"},
		{envRetentionBatch: "10000"},
		{envOutboxRetention: "1m"},
		{envEndpointConcurrency: "0"}, // zero disables the limit
	}
	for _, override := range cases {
		if _, err := FromEnv(envMap(withEnv(override))); err != nil {
			t.Errorf("boundary config %v rejected: %v", override, err)
		}
	}
}

// TestRuntimeEnvMalformedValues proves each typed parser reports a clear error
// for a malformed value.
func TestRuntimeEnvMalformedValues(t *testing.T) {
	cases := []struct {
		env   string
		value string
	}{
		{envWorkerConcurrency, "notanint"},
		{envMaxAttempts, "3.5"},
		{envWorkerLease, "45"}, // missing unit
		{envDeliveryTimeout, "soon"},
		{envEndpointRate, "fast"},
		{envProxyMode, "maybe"},
		{envEvaluationMode, "2"},
		{envObjectUseTLS, "yes-please"},
	}
	for _, c := range cases {
		t.Run(c.env+"="+c.value, func(t *testing.T) {
			_, err := FromEnv(envMap(withEnv(map[string]string{c.env: c.value})))
			if err == nil {
				t.Fatalf("expected parse error for %s=%s", c.env, c.value)
			}
			if !strings.Contains(err.Error(), c.env) {
				t.Fatalf("error %q does not mention %s", err.Error(), c.env)
			}
		})
	}
}
