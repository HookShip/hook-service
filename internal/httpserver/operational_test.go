package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
)

// TestOperationalRoutesServed proves the operational handler exposes the
// payload-free /statusz snapshot and the /metrics OpenMetrics endpoint in
// addition to the health probes.
func TestOperationalRoutesServed(t *testing.T) {
	t.Parallel()

	readiness := &Readiness{}
	readiness.Set(true)
	handler := newHandlerWithRoutes(readiness, buildinfo.Info{Version: "test"}, Options{
		StatusProvider: func() any { return map[string]any{"service": "hookshipd"} },
		MetricsWriter: func(w io.Writer) error {
			_, err := io.WriteString(w, "# TYPE hookship_x counter\nhookship_x_total 1\n# EOF\n")
			return err
		},
	})

	status := httptest.NewRecorder()
	handler.ServeHTTP(status, httptest.NewRequest(http.MethodGet, "/statusz", nil))
	if status.Code != http.StatusOK {
		t.Fatalf("/statusz status = %d", status.Code)
	}

	metrics := httptest.NewRecorder()
	handler.ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if metrics.Code != http.StatusOK {
		t.Fatalf("/metrics status = %d", metrics.Code)
	}
	if ct := metrics.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		t.Fatalf("/metrics Content-Type = %q", ct)
	}
	if !strings.Contains(metrics.Body.String(), "# EOF") {
		t.Fatalf("/metrics body missing EOF marker: %q", metrics.Body.String())
	}
}

// TestIngestSurfaceOmitsOperationalRoutes proves the public ingest listener
// (Register set, no StatusProvider/MetricsWriter) never exposes the operational
// surface, so status and metrics cannot leak on an externally bound listener.
func TestIngestSurfaceOmitsOperationalRoutes(t *testing.T) {
	t.Parallel()

	readiness := &Readiness{}
	readiness.Set(true)
	handler := newHandlerWithRoutes(readiness, buildinfo.Info{Version: "test"}, Options{
		Register: func(mux *http.ServeMux) {
			mux.HandleFunc("POST /v1/event-ingests", func(http.ResponseWriter, *http.Request) {})
		},
	})

	for _, path := range []string{"/statusz", "/metrics"} {
		recorder := httptest.NewRecorder()
		handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, path, nil))
		if recorder.Code != http.StatusNotFound {
			t.Fatalf("%s on the ingest surface = %d, want 404", path, recorder.Code)
		}
	}

	// Health and readiness must remain on the ingest surface (load balancers and
	// hookshipctl diagnostics probe them there).
	health := httptest.NewRecorder()
	handler.ServeHTTP(health, httptest.NewRequest(http.MethodGet, "/healthz", nil))
	if health.Code != http.StatusOK {
		t.Fatalf("/healthz on the ingest surface = %d, want 200", health.Code)
	}
}

func TestMetricsRejectsNonReadMethods(t *testing.T) {
	t.Parallel()

	handler := newHandlerWithRoutes(&Readiness{}, buildinfo.Info{Version: "test"}, Options{
		MetricsWriter: func(io.Writer) error { return nil },
	})
	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/metrics", nil))
	if recorder.Code != http.StatusMethodNotAllowed {
		t.Fatalf("POST /metrics status = %d", recorder.Code)
	}
	if got := recorder.Header().Get("Allow"); got != "GET, HEAD" {
		t.Fatalf("Allow = %q", got)
	}
}

// TestForcePlaintextDisablesTLS proves the operational listener stays plaintext
// even when a TLS certificate pair is configured for the ingest listener.
func TestForcePlaintextDisablesTLS(t *testing.T) {
	t.Parallel()

	certPath, keyPath := writeSelfSignedCert(t)
	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.OperationalAddress = "127.0.0.1:0"
	cfg.Runtime.Transport.TLSCertFile = certPath
	cfg.Runtime.Transport.TLSKeyFile = keyPath

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := NewWithOptions(cfg, logger, buildinfo.Info{Version: "test"}, Options{
		Address:        cfg.OperationalAddress,
		ForcePlaintext: true,
	})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}
	if server.TLSEnabled() {
		t.Fatal("operational listener must remain plaintext despite a configured TLS pair")
	}
	if server.listenAddress != cfg.OperationalAddress {
		t.Fatalf("listenAddress = %q, want the operational override", server.listenAddress)
	}
}

// TestBindHintSurfacedOnAddressInUse proves the operational listener turns a
// same-host address collision (the split api+worker footgun) into an actionable
// error instead of a bare "address already in use".
func TestBindHintSurfacedOnAddressInUse(t *testing.T) {
	t.Parallel()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()

	cfg := config.Default()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := NewWithOptions(cfg, logger, buildinfo.Info{Version: "test"}, Options{
		Address:        occupied.Addr().String(),
		ForcePlaintext: true,
		BindHint:       "set a distinct HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS for each",
	})
	if err != nil {
		t.Fatalf("NewWithOptions() error = %v", err)
	}

	err = server.Run(context.Background())
	if err == nil {
		t.Fatal("expected a bind error for the occupied address")
	}
	if !strings.Contains(err.Error(), "HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS") {
		t.Fatalf("bind error must include the actionable hint, got %v", err)
	}
}

// TestBindHintOmittedWithoutHint keeps the plain listen error when no hint is
// configured (for example, the ingest listener).
func TestBindHintOmittedWithoutHint(t *testing.T) {
	t.Parallel()

	occupied, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = occupied.Close() }()

	cfg := config.Default()
	cfg.ListenAddress = occupied.Addr().String()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	server, err := New(cfg, logger, buildinfo.Info{Version: "test"})
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	err = server.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("expected a plain listen error, got %v", err)
	}
	if strings.Contains(err.Error(), "HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS") {
		t.Fatalf("unhinted listener must not add the operational hint: %v", err)
	}
}
