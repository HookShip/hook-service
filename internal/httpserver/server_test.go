package httpserver

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/config"
)

func TestRunStopsAfterContextCancellation(t *testing.T) {
	t.Parallel()

	cfg := config.Default()
	cfg.ListenAddress = "127.0.0.1:0"
	cfg.ShutdownTimeout = time.Second
	server, err := New(
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		buildinfo.Info{Version: "test"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	result := make(chan error, 1)
	go func() {
		result <- server.Run(ctx)
	}()

	deadline := time.Now().Add(time.Second)
	for !server.readiness.Ready() && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if !server.readiness.Ready() {
		cancel()
		t.Fatal("server did not become ready")
	}

	cancel()
	select {
	case err := <-result:
		if err != nil {
			t.Fatalf("Run() error = %v", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run() did not stop after cancellation")
	}
	if server.readiness.Ready() {
		t.Fatal("server remained ready after shutdown")
	}
}

func TestRunReportsListenFailure(t *testing.T) {
	t.Parallel()

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserve listener: %v", err)
	}
	defer listener.Close()

	cfg := config.Default()
	cfg.ListenAddress = listener.Addr().String()
	server, err := New(
		cfg,
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		buildinfo.Info{Version: "test"},
	)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}

	err = server.Run(context.Background())
	if err == nil || !strings.Contains(err.Error(), "listen on") {
		t.Fatalf("Run() error = %v", err)
	}
}
