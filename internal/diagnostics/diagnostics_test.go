package diagnostics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRunSuccess(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/healthz":
			_, _ = w.Write([]byte(`{"status":"ok"}`))
		case "/readyz":
			_, _ = w.Write([]byte(`{"status":"ready"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	report, err := Run(context.Background(), server.Client(), server.URL, "hookshipctl/test")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if !report.OK || !report.Health.OK || !report.Readiness.OK {
		t.Fatalf("report = %+v", report)
	}
}

func TestRunReportsUnavailableReadiness(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/healthz" {
			_, _ = w.Write([]byte(`{"status":"ok"}`))
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = w.Write([]byte(`{"status":"not_ready"}`))
	}))
	defer server.Close()

	report, err := Run(context.Background(), server.Client(), server.URL, "")
	if err != nil {
		t.Fatalf("Run() error = %v", err)
	}
	if report.OK || report.Readiness.OK {
		t.Fatalf("report = %+v", report)
	}
	if !strings.Contains(report.Readiness.Error, `status "ready"`) {
		t.Fatalf("readiness error = %q", report.Readiness.Error)
	}
}

func TestRunRejectsUnsafeBaseURLs(t *testing.T) {
	t.Parallel()

	tests := []string{
		"ftp://example.com",
		"http://user@example.com",
		"http://example.com/base",
		"http://example.com?query=value",
		"example.com",
	}
	for _, rawURL := range tests {
		rawURL := rawURL
		t.Run(rawURL, func(t *testing.T) {
			t.Parallel()
			_, err := Run(context.Background(), http.DefaultClient, rawURL, "")
			if err == nil {
				t.Fatalf("Run(%q) error = nil", rawURL)
			}
		})
	}
}
