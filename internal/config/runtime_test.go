package config

import (
	"strings"
	"testing"
)

func envMap(values map[string]string) func(string) string {
	return func(name string) string { return values[name] }
}

func TestRuntimeMemoryRequiresEvaluation(t *testing.T) {
	_, err := FromEnv(envMap(map[string]string{
		envBackend: "memory",
	}))
	if err == nil || !strings.Contains(err.Error(), "evaluation") {
		t.Fatalf("expected evaluation requirement error, got %v", err)
	}
}

func TestRuntimeMemoryEvaluationLoopbackValid(t *testing.T) {
	cfg, err := FromEnv(envMap(map[string]string{
		envBackend:        "memory",
		envEvaluationMode: "true",
		envListenAddress:  "127.0.0.1:8080",
	}))
	if err != nil {
		t.Fatalf("expected valid memory evaluation config, got %v", err)
	}
	if !cfg.Runtime.Selected() || cfg.Runtime.Backend != "memory" {
		t.Fatalf("runtime not selected: %+v", cfg.Runtime)
	}
	if cfg.Runtime.Production() {
		t.Fatal("evaluation must not be production")
	}
}

func TestRuntimeDurableRequiresDatabaseAndKey(t *testing.T) {
	_, err := FromEnv(envMap(map[string]string{
		envBackend:   "postgres",
		envProxyMode: "true",
	}))
	if err == nil || !strings.Contains(err.Error(), "HOOKSHIP_DATABASE_URL") {
		t.Fatalf("expected database URL requirement, got %v", err)
	}

	_, err = FromEnv(envMap(map[string]string{
		envBackend:     "postgres",
		envProxyMode:   "true",
		envDatabaseURL: "postgres://localhost/hookship",
	}))
	if err == nil || !strings.Contains(err.Error(), envPayloadKey) {
		t.Fatalf("expected payload key requirement, got %v", err)
	}
}

func TestRuntimeRedisRequiresRedisURL(t *testing.T) {
	_, err := FromEnv(envMap(map[string]string{
		envBackend:     "postgres-redis",
		envProxyMode:   "true",
		envDatabaseURL: "postgres://localhost/hookship",
		envPayloadKey:  "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
	}))
	if err == nil || !strings.Contains(err.Error(), "Redis") {
		t.Fatalf("expected redis requirement, got %v", err)
	}
}

func TestRuntimeNonLoopbackPlaintextFailsClosed(t *testing.T) {
	_, err := FromEnv(envMap(map[string]string{
		envBackend:       "postgres",
		envDatabaseURL:   "postgres://localhost/hookship",
		envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		envListenAddress: "0.0.0.0:8080",
		// No TLS, no proxy mode: non-loopback plaintext must fail closed.
	}))
	if err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("expected non-loopback plaintext rejection, got %v", err)
	}
}

func TestRuntimeProxyModeAllowsNonLoopbackDurable(t *testing.T) {
	cfg, err := FromEnv(envMap(map[string]string{
		envBackend:       "postgres",
		envDatabaseURL:   "postgres://localhost/hookship",
		envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		envListenAddress: "0.0.0.0:8080",
		envProxyMode:     "true",
	}))
	if err != nil {
		t.Fatalf("proxy mode should permit non-loopback durable, got %v", err)
	}
	if len(cfg.Runtime.PayloadKey()) != 32 {
		t.Fatalf("payload key length = %d", len(cfg.Runtime.PayloadKey()))
	}
}

func TestRuntimePartialTLSPairRejected(t *testing.T) {
	cases := []map[string]string{
		{envTLSCert: "/tmp/cert.pem"},
		{envTLSKey: "/tmp/key.pem"},
	}
	for _, extra := range cases {
		values := map[string]string{
			envBackend:       "postgres",
			envDatabaseURL:   "postgres://localhost/hookship",
			envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
			envListenAddress: "0.0.0.0:8080",
		}
		for k, v := range extra {
			values[k] = v
		}
		_, err := FromEnv(envMap(values))
		if err == nil || !strings.Contains(err.Error(), "configured together") {
			t.Fatalf("expected partial TLS pair rejection, got %v", err)
		}
	}
}

func TestRuntimeTLSPairAllowsNonLoopbackDurable(t *testing.T) {
	cfg, err := FromEnv(envMap(map[string]string{
		envBackend:       "postgres",
		envDatabaseURL:   "postgres://localhost/hookship",
		envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		envListenAddress: "0.0.0.0:8080",
		envTLSCert:       "/tmp/server.crt",
		envTLSKey:        "/tmp/server.key",
	}))
	if err != nil {
		t.Fatalf("a complete TLS pair should permit non-loopback durable, got %v", err)
	}
	if !cfg.Runtime.HasTLS() {
		t.Fatal("HasTLS should be true when both cert and key are set")
	}
}

func TestRuntimeNoTLSNoProxyNonLoopbackDurableFailsClosed(t *testing.T) {
	_, err := FromEnv(envMap(map[string]string{
		envBackend:       "postgres",
		envDatabaseURL:   "postgres://localhost/hookship",
		envPayloadKey:    "000102030405060708090a0b0c0d0e0f101112131415161718191a1b1c1d1e1f",
		envListenAddress: "0.0.0.0:8080",
	}))
	if err == nil || !strings.Contains(err.Error(), "plaintext") {
		t.Fatalf("expected non-loopback plaintext durable to fail closed, got %v", err)
	}
}
