package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestRunVersion(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"-version"},
		func(string) string { return "" },
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "hookshipd dev ") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunCheckConfig(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"-check-config"},
		func(string) string { return "" },
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if stdout.String() != "hookshipd configuration is valid\n" {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		nil,
		func(name string) string {
			if name == "HOOKSHIP_LISTEN_ADDRESS" {
				return "invalid"
			}
			return ""
		},
		&stdout,
		&stderr,
	)
	if code != 2 {
		t.Fatalf("run() code = %d", code)
	}
	if !strings.Contains(stderr.String(), "invalid configuration") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunCheckConfigRejectsPartialTLSPair(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		args []string
		env  map[string]string
	}{
		{"bare cert only", nil, map[string]string{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_TLS_CERT": "/c"}},
		{"api key only", []string{"api"}, map[string]string{"HOOKSHIP_BACKEND": "postgres", "HOOKSHIP_TLS_KEY": "/k"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var stdout, stderr bytes.Buffer
			args := append(append([]string{}, tc.args...), "-check-config")
			code := run(context.Background(), args, func(name string) string { return tc.env[name] }, &stdout, &stderr)
			if code != 2 {
				t.Fatalf("code = %d, want 2; stderr=%s", code, stderr.String())
			}
			if !strings.Contains(stderr.String(), "configured together") {
				t.Fatalf("stderr = %q", stderr.String())
			}
		})
	}
}
