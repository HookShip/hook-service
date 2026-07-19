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
