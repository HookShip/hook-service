package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func fakeEnv(values map[string]string) func(string) string {
	return func(key string) string { return values[key] }
}

func TestDoctorAndValidateRejectPartialTLSPair(t *testing.T) {
	cases := []struct {
		name string
		env  map[string]string
	}{
		{"cert only", map[string]string{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_TLS_CERT": "/tmp/server.crt"}},
		{"key only", map[string]string{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_TLS_KEY": "/tmp/server.key"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			for _, invoke := range []struct {
				label string
				run   func(getenv func(string) string, stdout, stderr *bytes.Buffer) int
			}{
				{"doctor", func(getenv func(string) string, out, errb *bytes.Buffer) int {
					return runDoctor(context.Background(), nil, getenv, out, errb)
				}},
				{"backend validate", func(getenv func(string) string, out, errb *bytes.Buffer) int {
					return runBackendValidate(nil, getenv, out, errb)
				}},
			} {
				var out, errb bytes.Buffer
				code := invoke.run(fakeEnv(tc.env), &out, &errb)
				if code != 1 {
					t.Fatalf("%s %s: exit = %d, want 1; stderr=%s stdout=%s", invoke.label, tc.name, code, errb.String(), out.String())
				}
				if !strings.Contains(errb.String(), "configured together") {
					t.Fatalf("%s %s: message = %q", invoke.label, tc.name, errb.String())
				}
			}
		})
	}
}

func TestDoctorCompleteTLSPairIsNotTreatedAsPlaintext(t *testing.T) {
	// A complete cert/key pair on a non-loopback listener is secure: the
	// selection must NOT be flagged as plaintext-exposed.
	env := map[string]string{
		"HOOKSHIP_BACKEND":        "postgres",
		"HOOKSHIP_LISTEN_ADDRESS": "0.0.0.0:8080",
		"HOOKSHIP_TLS_CERT":       "/tmp/server.crt",
		"HOOKSHIP_TLS_KEY":        "/tmp/server.key",
	}
	var out, errb bytes.Buffer
	if code := runDoctor(context.Background(), nil, fakeEnv(env), &out, &errb); code != 0 {
		t.Fatalf("complete TLS pair doctor exit = %d, want 0; stderr=%s stdout=%s", code, errb.String(), out.String())
	}
}

func TestDoctorNonLoopbackPlaintextFailsClosed(t *testing.T) {
	env := map[string]string{
		"HOOKSHIP_BACKEND":        "postgres",
		"HOOKSHIP_LISTEN_ADDRESS": "0.0.0.0:8080",
		// No TLS, no proxy: plaintext exposed.
	}
	var out, errb bytes.Buffer
	code := runDoctor(context.Background(), nil, fakeEnv(env), &out, &errb)
	if code != 1 {
		t.Fatalf("plaintext exposed doctor exit = %d, want 1", code)
	}
	if !strings.Contains(out.String(), "non_loopback_plaintext_forbidden") {
		t.Fatalf("expected plaintext reason code, got %s", out.String())
	}
}

func TestDoctorProxyModeNonLoopbackIsValid(t *testing.T) {
	env := map[string]string{
		"HOOKSHIP_BACKEND":        "postgres",
		"HOOKSHIP_LISTEN_ADDRESS": "0.0.0.0:8080",
		"HOOKSHIP_PROXY_MODE":     "true",
	}
	var out, errb bytes.Buffer
	if code := runDoctor(context.Background(), nil, fakeEnv(env), &out, &errb); code != 0 {
		t.Fatalf("proxy-mode doctor exit = %d, want 0; stdout=%s", code, out.String())
	}
}

func TestDoctorLoopbackPlaintextIsValid(t *testing.T) {
	env := map[string]string{
		"HOOKSHIP_BACKEND":         "memory",
		"HOOKSHIP_EVALUATION_MODE": "true",
		"HOOKSHIP_LISTEN_ADDRESS":  "127.0.0.1:8080",
	}
	var out, errb bytes.Buffer
	if code := runDoctor(context.Background(), nil, fakeEnv(env), &out, &errb); code != 0 {
		t.Fatalf("loopback evaluation doctor exit = %d, want 0; stdout=%s stderr=%s", code, out.String(), errb.String())
	}
}

func TestBackendValidateCompletePairAndProxy(t *testing.T) {
	for _, env := range []map[string]string{
		{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_LISTEN_ADDRESS": "0.0.0.0:8080", "HOOKSHIP_TLS_CERT": "/c", "HOOKSHIP_TLS_KEY": "/k"},
		{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_LISTEN_ADDRESS": "0.0.0.0:8080", "HOOKSHIP_PROXY_MODE": "true"},
	} {
		var out, errb bytes.Buffer
		if code := runBackendValidate(nil, fakeEnv(env), &out, &errb); code != 0 {
			t.Fatalf("backend validate exit = %d, want 0; stdout=%s", code, out.String())
		}
	}
}
