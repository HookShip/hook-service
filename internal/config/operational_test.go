package config

import "testing"

func TestOperationalAddressDefaultsToLoopback(t *testing.T) {
	t.Parallel()
	cfg := Default()
	if cfg.OperationalAddress != defaultOperationalAddr {
		t.Fatalf("default operational address = %q, want %q", cfg.OperationalAddress, defaultOperationalAddr)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("default config must validate, got %v", err)
	}
}

func TestOperationalAddressFailsClosedOffLoopback(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{
		"0.0.0.0:9090",
		"10.0.0.5:9090",
		"192.168.1.10:9090",
		"[::]:9090",
	} {
		cfg := Default()
		cfg.OperationalAddress = addr
		if err := cfg.Validate(); err == nil {
			t.Fatalf("operational address %q must fail closed as non-loopback", addr)
		}
	}
}

func TestOperationalAddressAcceptsLoopbackForms(t *testing.T) {
	t.Parallel()
	for _, addr := range []string{
		"127.0.0.1:0",
		"127.0.0.1:9099",
		"localhost:9091",
		"[::1]:9090",
	} {
		cfg := Default()
		cfg.OperationalAddress = addr
		if err := cfg.Validate(); err != nil {
			t.Fatalf("loopback operational address %q must validate, got %v", addr, err)
		}
	}
}

func TestOperationalAddressMustDifferFromListener(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.ListenAddress = "127.0.0.1:9090"
	cfg.OperationalAddress = "127.0.0.1:9090"
	if err := cfg.Validate(); err == nil {
		t.Fatal("operational address equal to the ingest listener must be rejected")
	}
}

func TestOperationalAddressRejectsMalformed(t *testing.T) {
	t.Parallel()
	cfg := Default()
	cfg.OperationalAddress = "not-an-address"
	if err := cfg.Validate(); err == nil {
		t.Fatal("malformed operational address must be rejected")
	}
}

func TestOperationalAddressFromEnvOverride(t *testing.T) {
	t.Parallel()
	env := map[string]string{"HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS": "127.0.0.1:9191"}
	cfg, err := FromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.OperationalAddress != "127.0.0.1:9191" {
		t.Fatalf("operational address = %q, want the env override", cfg.OperationalAddress)
	}
}
