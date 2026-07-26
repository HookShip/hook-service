package daemon_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/daemon"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func memoryConfig(t *testing.T, listenAddr string) config.Config {
	t.Helper()
	env := map[string]string{
		"HOOKSHIP_BACKEND":         "memory",
		"HOOKSHIP_EVALUATION_MODE": "true",
		"HOOKSHIP_LISTEN_ADDRESS":  listenAddr,
		// The operational listener binds an ephemeral loopback port so parallel
		// daemon tests never collide on the fixed default operational port.
		"HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS": "127.0.0.1:0",
		"HOOKSHIP_SINK_URL":                   "http://127.0.0.1:9099/sink",
		"HOOKSHIP_OUTBOX_SINK":                "log",
		// Keep the poll interval snappy so an idle worker stops promptly on cancel.
		"HOOKSHIP_WORKER_LEASE": "5s",
	}
	cfg, err := config.FromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("build config: %v", err)
	}
	return cfg
}

// occupyPort binds an ephemeral port and returns its address plus a listener
// that keeps it occupied until closed.
func occupyPort(t *testing.T) (string, net.Listener) {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	return listener.Addr().String(), listener
}

func TestRunAllModeReturnsPromptlyWhenListenFails(t *testing.T) {
	addr, occupied := occupyPort(t)
	defer occupied.Close()
	cfg := memoryConfig(t, addr)

	// The parent context is never canceled: Run must return solely because the
	// listener bind fails, not because of shutdown. Before the fix the delivery
	// worker kept waiting on the parent context and Run hung here.
	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(context.Background(), daemon.ModeAll, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected a non-nil bind error")
		}
		if !strings.Contains(err.Error(), "listen") {
			t.Fatalf("expected a listen error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon.Run hung after the server failed to bind (deadlock)")
	}
}

func TestRunWorkerModeReturnsPromptlyWhenListenFails(t *testing.T) {
	addr, occupied := occupyPort(t)
	defer occupied.Close()
	cfg := memoryConfig(t, addr)

	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(context.Background(), daemon.ModeWorker, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()

	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected a non-nil bind error in worker mode")
		}
		if !strings.Contains(err.Error(), "listen") {
			t.Fatalf("expected a listen error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("worker-mode daemon.Run hung after the health server failed to bind")
	}
}

func TestRunGracefulCancellationReturnsNil(t *testing.T) {
	cfg := memoryConfig(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())

	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(ctx, daemon.ModeAll, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()

	// Give the server and worker time to start, then trigger graceful shutdown.
	time.Sleep(300 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("graceful shutdown must return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("graceful shutdown hung")
	}
}

func TestRunAPIOnlyGracefulCancellationReturnsNil(t *testing.T) {
	cfg := memoryConfig(t, "127.0.0.1:0")
	ctx, cancel := context.WithCancel(context.Background())

	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(ctx, daemon.ModeAPI, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()
	time.Sleep(200 * time.Millisecond)
	cancel()

	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("api-only graceful shutdown must return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("api-only graceful shutdown hung")
	}
}

func TestRunAPIOnlyReturnsPromptlyWhenListenFails(t *testing.T) {
	addr, occupied := occupyPort(t)
	defer occupied.Close()
	cfg := memoryConfig(t, addr)

	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(context.Background(), daemon.ModeAPI, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()
	select {
	case err := <-result:
		if err == nil || !strings.Contains(err.Error(), "listen") {
			t.Fatalf("expected api-only listen error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("api-only daemon.Run hung after bind failure")
	}
}

// TestRunWorkerSetupFailureAfterServerLaunchReturnsOriginalError reproduces the
// post-launch setup-error path: the API server binds and launches successfully,
// then worker setup fails (an unwritable outbox sink path). Run must cancel and
// join the live server, close resources, and return the original setup error
// promptly rather than hanging or swallowing it.
func TestRunWorkerSetupFailureAfterServerLaunchReturnsOriginalError(t *testing.T) {
	cfg := memoryConfig(t, "127.0.0.1:0")
	// A file path whose parent directory does not exist makes buildWorker's sink
	// creation fail after the server has already been launched.
	cfg.Runtime.Sink.OutboxSink = filepath.Join(t.TempDir(), "does-not-exist", "observations.jsonl")

	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(context.Background(), daemon.ModeAll, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()
	select {
	case err := <-result:
		if err == nil {
			t.Fatal("expected the worker setup error to be returned")
		}
		if strings.Contains(err.Error(), "listen") {
			t.Fatalf("expected a sink setup error, not a listen error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon.Run hung on a post-launch worker setup failure")
	}
}

// freePort reserves and immediately releases an ephemeral loopback port so the
// caller can bind it explicitly. The small reuse window is acceptable for tests
// that need to probe a known address.
func freePort(t *testing.T) string {
	t.Helper()
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := listener.Addr().String()
	_ = listener.Close()
	return addr
}

func waitForReady(t *testing.T, url string) {
	t.Helper()
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		response, err := client.Get(url)
		if err == nil {
			_ = response.Body.Close()
			if response.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("service did not become ready at %s", url)
}

func getStatus(t *testing.T, url string) (int, http.Header, string) {
	t.Helper()
	response, err := (&http.Client{Timeout: time.Second}).Get(url)
	if err != nil {
		t.Fatalf("GET %s: %v", url, err)
	}
	defer func() { _ = response.Body.Close() }()
	body, _ := io.ReadAll(io.LimitReader(response.Body, 1<<16))
	return response.StatusCode, response.Header, string(body)
}

// TestOperationalSurfaceSeparation proves end-to-end that the public ingest
// listener never serves /statusz or /metrics, while the separate loopback
// operational listener serves both plus the health probes.
func TestOperationalSurfaceSeparation(t *testing.T) {
	mainAddr := freePort(t)
	opAddr := freePort(t)
	env := map[string]string{
		"HOOKSHIP_BACKEND":                    "memory",
		"HOOKSHIP_EVALUATION_MODE":            "true",
		"HOOKSHIP_LISTEN_ADDRESS":             mainAddr,
		"HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS": opAddr,
		"HOOKSHIP_OUTBOX_SINK":                "log",
		"HOOKSHIP_WORKER_LEASE":               "5s",
	}
	cfg, err := config.FromEnv(func(key string) string { return env[key] })
	if err != nil {
		t.Fatalf("build config: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- daemon.Run(ctx, daemon.ModeAll, cfg, discardLogger(), buildinfo.Info{Version: "test"})
	}()

	waitForReady(t, "http://"+mainAddr+"/readyz")

	// The public ingest surface must not expose the operational endpoints.
	for _, path := range []string{"/statusz", "/metrics"} {
		if code, _, _ := getStatus(t, "http://"+mainAddr+path); code != http.StatusNotFound {
			cancel()
			t.Fatalf("public %s = %d, want 404", path, code)
		}
	}
	if code, _, _ := getStatus(t, "http://"+mainAddr+"/healthz"); code != http.StatusOK {
		cancel()
		t.Fatalf("public /healthz = %d, want 200", code)
	}

	// The operational surface serves status and metrics.
	if code, _, body := getStatus(t, "http://"+opAddr+"/statusz"); code != http.StatusOK || !strings.Contains(body, "hookshipd") {
		cancel()
		t.Fatalf("operational /statusz = %d body=%q", code, body)
	}
	code, header, body := getStatus(t, "http://"+opAddr+"/metrics")
	if code != http.StatusOK {
		cancel()
		t.Fatalf("operational /metrics = %d", code)
	}
	if ct := header.Get("Content-Type"); !strings.HasPrefix(ct, "application/openmetrics-text") {
		cancel()
		t.Fatalf("operational /metrics Content-Type = %q", ct)
	}
	if !strings.Contains(body, "# EOF") {
		cancel()
		t.Fatalf("operational /metrics missing EOF marker: %q", body)
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("graceful shutdown must return nil, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("daemon.Run hung on shutdown")
	}
}
