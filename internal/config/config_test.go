package config

import (
	"strings"
	"testing"
	"time"
)

func TestFromEnvDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := FromEnv(func(string) string { return "" })
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ListenAddress != "127.0.0.1:8080" {
		t.Fatalf("ListenAddress = %q", cfg.ListenAddress)
	}
	if cfg.ReadHeader != 5*time.Second {
		t.Fatalf("ReadHeader = %s", cfg.ReadHeader)
	}
	if cfg.MaxHeaderBytes != 64<<10 {
		t.Fatalf("MaxHeaderBytes = %d", cfg.MaxHeaderBytes)
	}
}

func TestFromEnvOverrides(t *testing.T) {
	t.Parallel()

	values := map[string]string{
		envListenAddress:     "0.0.0.0:9090",
		envShutdownTimeout:   "20s",
		envReadHeaderTimeout: "2s",
		envReadTimeout:       "3s",
		envWriteTimeout:      "4s",
		envIdleTimeout:       "30s",
		envMaxHeaderBytes:    "8192",
		envLogLevel:          "DEBUG",
	}
	cfg, err := FromEnv(func(name string) string { return values[name] })
	if err != nil {
		t.Fatalf("FromEnv() error = %v", err)
	}
	if cfg.ListenAddress != values[envListenAddress] {
		t.Fatalf("ListenAddress = %q", cfg.ListenAddress)
	}
	if cfg.ShutdownTimeout != 20*time.Second {
		t.Fatalf("ShutdownTimeout = %s", cfg.ShutdownTimeout)
	}
	if cfg.MaxHeaderBytes != 8192 {
		t.Fatalf("MaxHeaderBytes = %d", cfg.MaxHeaderBytes)
	}
	if cfg.LogLevel != "debug" {
		t.Fatalf("LogLevel = %q", cfg.LogLevel)
	}
}

func TestFromEnvRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		key     string
		value   string
		wantErr string
	}{
		{name: "address", key: envListenAddress, value: "localhost", wantErr: "host:port"},
		{name: "duration", key: envReadTimeout, value: "soon", wantErr: "Go duration"},
		{name: "zero duration", key: envIdleTimeout, value: "0s", wantErr: "greater than zero"},
		{name: "excessive duration", key: envShutdownTimeout, value: "6m", wantErr: "must not exceed"},
		{name: "header integer", key: envMaxHeaderBytes, value: "large", wantErr: "integer"},
		{name: "header bound", key: envMaxHeaderBytes, value: "1024", wantErr: "between"},
		{name: "log level", key: envLogLevel, value: "verbose", wantErr: "debug, info, warn, or error"},
	}

	for _, test := range tests {
		test := test
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			_, err := FromEnv(func(name string) string {
				if name == test.key {
					return test.value
				}
				return ""
			})
			if err == nil || !strings.Contains(err.Error(), test.wantErr) {
				t.Fatalf("FromEnv() error = %v, want containing %q", err, test.wantErr)
			}
		})
	}
}
