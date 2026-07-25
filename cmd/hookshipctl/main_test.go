package main

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunVersionJSON(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(context.Background(), []string{"version", "-output=json"}, &stdout, &stderr)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), `"version": "dev"`) {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunDiagnostics(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		status := "ok"
		if r.URL.Path == "/readyz" {
			status = "ready"
		}
		_, _ = w.Write([]byte(`{"status":"` + status + `"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"diagnostics", "-url=" + server.URL},
		&stdout,
		&stderr,
	)
	if code != 0 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "overall: ok") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunDiagnosticsFailure(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}))
	defer server.Close()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	code := run(
		context.Background(),
		[]string{"diagnostics", "-url=" + server.URL},
		&stdout,
		&stderr,
	)
	if code != 1 {
		t.Fatalf("run() code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.Contains(stdout.String(), "overall: failed") {
		t.Fatalf("stdout = %q", stdout.String())
	}
}

func TestRunVersionText(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version"}, &stdout, &stderr); code != 0 {
		t.Fatalf("version text code = %d, stderr = %q", code, stderr.String())
	}
	if !strings.HasPrefix(stdout.String(), "hookshipctl ") {
		t.Fatalf("version text output = %q", stdout.String())
	}
}

func TestRunVersionRejectsBadOutput(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version", "-output=yaml"}, &stdout, &stderr); code != 2 {
		t.Fatalf("bad output code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "text or json") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}

func TestRunVersionRejectsExtraArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"version", "extra"}, &stdout, &stderr); code != 2 {
		t.Fatalf("extra args code = %d, want 2", code)
	}
}

func TestRunHelp(t *testing.T) {
	for _, arg := range []string{"help", "-h", "--help"} {
		var stdout, stderr bytes.Buffer
		if code := run(context.Background(), []string{arg}, &stdout, &stderr); code != 0 {
			t.Fatalf("%s code = %d, want 0", arg, code)
		}
		if !strings.Contains(stdout.String(), "hookshipctl") {
			t.Fatalf("%s usage missing, got %q", arg, stdout.String())
		}
	}
}

func TestRunNoArgs(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), nil, &stdout, &stderr); code != 2 {
		t.Fatalf("no-args code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "hookshipctl") {
		t.Fatalf("no-args usage missing, got %q", stderr.String())
	}
}

func TestRunUnknownCommand(t *testing.T) {
	var stdout, stderr bytes.Buffer
	if code := run(context.Background(), []string{"frobnicate"}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown command code = %d, want 2", code)
	}
	if !strings.Contains(stderr.String(), "unknown command") {
		t.Fatalf("stderr = %q", stderr.String())
	}
}
