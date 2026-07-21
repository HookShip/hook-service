package daemon

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
)

// lifecycle coordinates the daemon's concurrent components so that the first
// component (or a post-launch setup error) to fail cancels its peers, every
// launched goroutine is joined, and resource closers run only after the join.
// It is the single shared finish path for both normal completion and error
// teardown, avoiding a deadlock when there are no goroutines and avoiding a race
// between resource close and still-running goroutines.
type lifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc
	logger *slog.Logger

	group   sync.WaitGroup
	mu      sync.Mutex
	fatal   error
	closers []func()
}

func newLifecycle(ctx context.Context, cancel context.CancelFunc, logger *slog.Logger) *lifecycle {
	if logger == nil {
		logger = slog.Default()
	}
	return &lifecycle{ctx: ctx, cancel: cancel, logger: logger}
}

// runContext is the child context passed to every component.
func (l *lifecycle) runContext() context.Context { return l.ctx }

// setFatal records the first fatal error only.
func (l *lifecycle) setFatal(err error) {
	if err == nil {
		return
	}
	l.mu.Lock()
	if l.fatal == nil {
		l.fatal = err
	}
	l.mu.Unlock()
}

func (l *lifecycle) firstFatal() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.fatal
}

// addCloser registers a resource closer to run (in LIFO order) after every
// launched goroutine has joined. It is only called from the setup goroutine
// before finish, so the slice needs no locking.
func (l *lifecycle) addCloser(fn func()) {
	l.closers = append(l.closers, fn)
}

// launchCritical runs an essential component (API/health server, delivery
// worker). The first to return records the first fatal error and cancels the
// peers so they stop and are joined.
func (l *lifecycle) launchCritical(name string, run func(context.Context) error) {
	l.group.Add(1)
	go func() {
		defer l.group.Done()
		defer l.cancel()
		if err := run(l.ctx); err != nil {
			l.setFatal(fmt.Errorf("%s: %w", name, err))
		}
	}()
}

// launchAux runs a best-effort auxiliary component (dispatch relay). Its failure
// is logged, never fatal, and never cancels peers; it stops when the run context
// is canceled and is joined by the wait group.
func (l *lifecycle) launchAux(name string, run func(context.Context) error) {
	l.group.Add(1)
	go func() {
		defer l.group.Done()
		if err := run(l.ctx); err != nil && l.ctx.Err() == nil {
			l.logger.Warn(name+" stopped", slog.String("error", err.Error()))
		}
	}()
}

// finish is the single shared teardown path. A non-nil setupErr (a failure after
// one or more components were already launched) is preserved as the first fatal
// error and proactively cancels the peers. It then joins every launched
// goroutine and only afterwards runs the resource closers in LIFO order, so a
// closer never races a still-running goroutine. With no goroutines it returns
// immediately without deadlocking.
func (l *lifecycle) finish(setupErr error) error {
	if setupErr != nil {
		l.setFatal(setupErr)
		l.cancel()
	}
	l.group.Wait()
	for i := len(l.closers) - 1; i >= 0; i-- {
		l.closers[i]()
	}
	return l.firstFatal()
}
