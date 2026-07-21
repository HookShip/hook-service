// Package daemon wires the selected backend, ingest handler, and delivery
// worker into the api, worker, and all runtime modes.
package daemon

import (
	"context"
	"errors"
	"log/slog"
	"time"

	"github.com/hookship/hook-service/internal/backend"
	"github.com/hookship/hook-service/internal/buildinfo"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/httpserver"
	"github.com/hookship/hook-service/internal/ingest"
	"github.com/hookship/hook-service/internal/outboxsink"
	"github.com/hookship/hook-service/internal/runtime"
	"github.com/hookship/hook-service/internal/transport"
	"github.com/hookship/hook-service/internal/worker"
)

// Mode selects the process role.
type Mode string

const (
	ModeAPI    Mode = "api"
	ModeWorker Mode = "worker"
	ModeAll    Mode = "all"
)

// Run assembles and runs the requested mode until the context is canceled.
func Run(ctx context.Context, mode Mode, cfg config.Config, logger *slog.Logger, info buildinfo.Info) error {
	if !cfg.Runtime.Selected() {
		return errors.New("no backend selected; set HOOKSHIP_BACKEND")
	}
	assignments, err := backend.ResolveAssignments(cfg.Runtime)
	if err != nil {
		return err
	}
	clock := runtime.SystemClock{}
	ids := runtime.NewULIDSource(clock)

	built, err := backend.Build(ctx, backend.Deps{
		Runtime:     cfg.Runtime,
		Assignments: assignments,
		Clock:       clock,
		IDs:         ids,
		Logger:      logger,
	})
	if err != nil {
		return err
	}

	logStartupGuards(logger, cfg.Runtime, built)
	durability := durabilityLabel(cfg.Runtime.Backend)

	// A child context ensures the first component to return (for example a bind,
	// Serve, or TLS failure) or a post-launch setup error cancels its peers so
	// they stop and are joined, instead of a component hanging on the shared
	// parent context while the fatal error is swallowed. Parent-context (signal)
	// cancellation still triggers graceful shutdown. All post-build teardown —
	// including built.Close and every resource closer — funnels through
	// lifecycle.finish, which joins goroutines before closing resources.
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	life := newLifecycle(runCtx, cancel, logger)
	life.addCloser(built.Close)

	if mode == ModeAPI || mode == ModeAll {
		server, err := buildServer(cfg, logger, info, built, ids, clock, durability)
		if err != nil {
			return life.finish(err)
		}
		life.launchCritical("api server", server.Run)
	} else {
		server, err := httpserver.NewWithOptions(cfg, logger, info, httpserver.Options{
			Durability:     durability,
			ReadinessProbe: backendProbe(built),
		})
		if err != nil {
			return life.finish(err)
		}
		life.launchCritical("worker health server", server.Run)
	}

	if mode == ModeWorker || mode == ModeAll {
		relayComp, err := buildRelay(life.runContext(), cfg.Runtime, built, logger)
		if err != nil {
			return life.finish(err)
		}
		var wake <-chan struct{}
		if relayComp != nil {
			life.addCloser(relayComp.close)
			wake = relayComp.wake
			relayComp.launch(life.launchAux)
		}
		deliveryWorker, sink, err := buildWorker(cfg, logger, info, built, ids, clock, wake)
		if err != nil {
			return life.finish(err)
		}
		life.addCloser(func() { _ = sink.Close() })
		life.launchCritical("delivery worker", deliveryWorker.Run)
	}

	return life.finish(nil)
}

func buildServer(
	cfg config.Config,
	logger *slog.Logger,
	info buildinfo.Info,
	built runtime.Backend,
	ids runtime.IDSource,
	clock runtime.Clock,
	durability string,
) (*httpserver.Server, error) {
	handler, err := ingest.New(ingest.Options{
		Backend:                built,
		Clock:                  clock,
		IDs:                    ids,
		Logger:                 logger,
		RequireSecureTransport: !cfg.Runtime.EvaluationMode && !cfg.Runtime.ProxyMode,
		EvaluationMode:         cfg.Runtime.EvaluationMode,
		TrustProxy:             cfg.Runtime.ProxyMode,
	})
	if err != nil {
		return nil, err
	}
	return httpserver.NewWithOptions(cfg, logger, info, httpserver.Options{
		Register:       handler.Register,
		ReadinessProbe: backendProbe(built),
		Durability:     durability,
	})
}

func buildWorker(
	cfg config.Config,
	logger *slog.Logger,
	info buildinfo.Info,
	built runtime.Backend,
	ids runtime.IDSource,
	clock runtime.Clock,
	wake <-chan struct{},
) (*worker.Worker, outboxsink.Sink, error) {
	sink, err := buildSink(cfg.Runtime, logger)
	if err != nil {
		return nil, nil, err
	}
	client := transport.NewClient(transport.Options{
		TotalTimeout:   cfg.Runtime.DeliveryTimeout,
		AllowLoopback:  cfg.Runtime.AllowLoopbackDelivery(),
		AllowPrivate:   cfg.Runtime.AllowPrivateDelivery(),
		AllowPlaintext: cfg.Runtime.AllowPlaintextDelivery(),
		UserAgent:      "hookship-delivery/" + info.Version,
	})
	deliveryWorker, err := worker.New(worker.Options{
		Backend:         built,
		Transport:       client,
		Sink:            sink,
		Clock:           clock,
		IDs:             ids,
		Logger:          logger,
		Concurrency:     cfg.Runtime.WorkerConcurrency,
		LeaseDuration:   cfg.Runtime.WorkerLease,
		DeliveryTimeout: cfg.Runtime.DeliveryTimeout,
		Wake:            wake,
	})
	if err != nil {
		_ = sink.Close()
		return nil, nil, err
	}
	return deliveryWorker, sink, nil
}

func buildSink(runtimeCfg config.RuntimeConfig, logger *slog.Logger) (outboxsink.Sink, error) {
	if runtimeCfg.MetadataSinkURL != "" {
		return outboxsink.NewPlatformSink(outboxsink.PlatformOptions{
			URL:     runtimeCfg.MetadataSinkURL,
			Token:   runtimeCfg.MetadataSinkToken,
			Timeout: runtimeCfg.DeliveryTimeout,
		})
	}
	switch runtimeCfg.OutboxSink {
	case "", "none":
		if runtimeCfg.EvaluationMode {
			return outboxsink.NewLocalLogSink(logger), nil
		}
		return outboxsink.NoopSink{}, nil
	case "log":
		return outboxsink.NewLocalLogSink(logger), nil
	default:
		return outboxsink.NewLocalFileSink(runtimeCfg.OutboxSink)
	}
}

func backendProbe(built runtime.Backend) func() bool {
	return func() bool {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		return built.Ping(ctx) == nil
	}
}

func durabilityLabel(profile string) string {
	document, ok := capabilities.Lookup(profile)
	if !ok {
		return ""
	}
	return string(document.Properties.Durability)
}

func logStartupGuards(logger *slog.Logger, runtimeCfg config.RuntimeConfig, built runtime.Backend) {
	if !built.Durable() {
		logger.Warn("evaluation backend selected: accepted events are ephemeral and lost on restart",
			slog.String("backend", built.Name()),
			slog.String("durability", "ephemeral"),
		)
	}
	if runtimeCfg.EvaluationMode {
		logger.Warn("evaluation mode enabled: loopback-only, non-production credentials", slog.String("backend", built.Name()))
	}
}
