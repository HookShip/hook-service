package daemon

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"
)

func testLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// runWithin fails the test if fn does not return within the timeout, catching a
// finish() deadlock.
func runWithin(t *testing.T, timeout time.Duration, fn func() error) error {
	t.Helper()
	done := make(chan error, 1)
	go func() { done <- fn() }()
	select {
	case err := <-done:
		return err
	case <-time.After(timeout):
		t.Fatal("lifecycle operation deadlocked")
		return nil
	}
}

func TestLifecycleSetupErrorCancelsJoinsThenCloses(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())

	var serverStopped atomic.Bool
	var closerSawServerStopped atomic.Bool
	var closerSawBackendLast atomic.Bool
	var closeOrder []string

	// built.Close registered first → must run LAST (after the sink/relay closers).
	life.addCloser(func() {
		closeOrder = append(closeOrder, "backend")
		// The server goroutine must already have stopped before any closer runs.
		closerSawServerStopped.Store(serverStopped.Load())
	})

	// A live "server" that blocks until the run context is canceled.
	life.launchCritical("api server", func(runCtx context.Context) error {
		<-runCtx.Done()
		serverStopped.Store(true)
		return nil
	})

	// A resource closer that must run before "backend" (LIFO) and only after the
	// server has joined.
	life.addCloser(func() {
		closeOrder = append(closeOrder, "sink")
		closerSawBackendLast.Store(serverStopped.Load())
	})

	setupErr := errors.New("worker setup failed after server launch")
	err := runWithin(t, 3*time.Second, func() error { return life.finish(setupErr) })

	if !errors.Is(err, setupErr) {
		t.Fatalf("finish returned %v, want the original setup error", err)
	}
	if !serverStopped.Load() {
		t.Fatal("server was not joined before finish returned")
	}
	if !closerSawServerStopped.Load() || !closerSawBackendLast.Load() {
		t.Fatal("a resource closer ran before the server goroutine stopped (race)")
	}
	if len(closeOrder) != 2 || closeOrder[0] != "sink" || closeOrder[1] != "backend" {
		t.Fatalf("closers ran in the wrong order: %v (want [sink backend])", closeOrder)
	}
}

func TestLifecycleAuxComponentCanceledAndJoinedBeforeClose(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())

	var serverStopped, relayStopped atomic.Bool
	var relayCloserSawRelayStopped atomic.Bool

	life.addCloser(func() {}) // stand-in for built.Close

	life.launchCritical("api server", func(runCtx context.Context) error {
		<-runCtx.Done()
		serverStopped.Store(true)
		return nil
	})

	// The relay closer must run only after the relay goroutine has joined.
	life.addCloser(func() { relayCloserSawRelayStopped.Store(relayStopped.Load()) })
	life.launchAux("dispatch relayer", func(runCtx context.Context) error {
		<-runCtx.Done()
		relayStopped.Store(true)
		return nil
	})

	setupErr := errors.New("post-launch setup failure")
	err := runWithin(t, 3*time.Second, func() error { return life.finish(setupErr) })
	if !errors.Is(err, setupErr) {
		t.Fatalf("finish returned %v, want setup error", err)
	}
	if !serverStopped.Load() || !relayStopped.Load() {
		t.Fatal("server or relay was not joined before finish returned")
	}
	if !relayCloserSawRelayStopped.Load() {
		t.Fatal("relay closer ran before the relay goroutine stopped (race)")
	}
}

func TestLifecycleFirstFatalPrefersComponentError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())

	componentErr := errors.New("bind failed")
	life.launchCritical("api server", func(context.Context) error {
		return componentErr
	})

	// finish(nil): the failing component recorded the fatal error and canceled;
	// finish joins it and returns that error.
	err := runWithin(t, 3*time.Second, func() error { return life.finish(nil) })
	if !errors.Is(err, componentErr) {
		t.Fatalf("finish returned %v, want the component error", err)
	}
}

func TestLifecycleSetupErrorPreservedOverGracefulComponents(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())

	life.launchCritical("api server", func(runCtx context.Context) error {
		<-runCtx.Done()
		return nil // graceful
	})

	setupErr := errors.New("relay setup failed")
	err := runWithin(t, 3*time.Second, func() error { return life.finish(setupErr) })
	if !errors.Is(err, setupErr) {
		t.Fatalf("finish returned %v, want the setup error", err)
	}
}

func TestLifecycleAuxErrorIsNotFatal(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())

	life.launchAux("dispatch relayer", func(context.Context) error {
		return errors.New("broker unreachable")
	})

	// An auxiliary failure is logged, not fatal; finish(nil) returns nil.
	err := runWithin(t, 3*time.Second, func() error { return life.finish(nil) })
	if err != nil {
		t.Fatalf("auxiliary failure must not be fatal, got %v", err)
	}
}

func TestLifecycleNoGoroutinesDoesNotDeadlock(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	life := newLifecycle(ctx, cancel, testLogger())
	var closed atomic.Bool
	life.addCloser(func() { closed.Store(true) })

	err := runWithin(t, time.Second, func() error { return life.finish(nil) })
	if err != nil {
		t.Fatalf("finish(nil) with no goroutines returned %v", err)
	}
	if !closed.Load() {
		t.Fatal("closer did not run")
	}

	// A setup error with no goroutines returns promptly with that error.
	ctx2, cancel2 := context.WithCancel(context.Background())
	life2 := newLifecycle(ctx2, cancel2, testLogger())
	setupErr := errors.New("setup failed before launch")
	err = runWithin(t, time.Second, func() error { return life2.finish(setupErr) })
	if !errors.Is(err, setupErr) {
		t.Fatalf("finish(setupErr) returned %v", err)
	}
}
