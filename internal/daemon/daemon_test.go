package daemon_test

import (
	"context"
	"io"
	"log/slog"
	"net"
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
		"HOOKSHIP_SINK_URL":        "http://127.0.0.1:9099/sink",
		"HOOKSHIP_OUTBOX_SINK":     "log",
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
	cfg.Runtime.OutboxSink = filepath.Join(t.TempDir(), "does-not-exist", "observations.jsonl")

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
