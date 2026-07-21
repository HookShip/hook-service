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
	"github.com/hookship/hook-service/internal/maintenance"
	"github.com/hookship/hook-service/internal/metrics"
	"github.com/hookship/hook-service/internal/outboxsink"
	"github.com/hookship/hook-service/internal/ratelimit"
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
	registry := metrics.NewRegistry()
	status := operatorStatus(registry, cfg.Runtime, built, durability, info)

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
		server, err := buildServer(cfg, logger, info, built, ids, clock, durability, status)
		if err != nil {
			return life.finish(err)
		}
		life.launchCritical("api server", server.Run)
	} else {
		server, err := httpserver.NewWithOptions(cfg, logger, info, httpserver.Options{
			Durability:     durability,
			ReadinessProbe: backendProbe(built),
			StatusProvider: status,
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
		deliveryWorker, sink, err := buildWorker(cfg, logger, info, built, ids, clock, registry, wake)
		if err != nil {
			return life.finish(err)
		}
		life.addCloser(func() { _ = sink.Close() })
		life.launchCritical("delivery worker", deliveryWorker.Run)

		if retentionWorker := buildMaintenance(cfg.Runtime, logger, clock, registry, built); retentionWorker != nil {
			life.launchAux("retention worker", retentionWorker.Run)
		}
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
	status func() any,
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
		StatusProvider: status,
	})
}

func buildWorker(
	cfg config.Config,
	logger *slog.Logger,
	info buildinfo.Info,
	built runtime.Backend,
	ids runtime.IDSource,
	clock runtime.Clock,
	registry *metrics.Registry,
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
	limiter := ratelimit.New(ratelimit.Config{
		AssignmentConcurrency: cfg.Runtime.AssignmentConcurrency,
		EndpointConcurrency:   cfg.Runtime.EndpointConcurrency,
		AssignmentRatePerSec:  cfg.Runtime.AssignmentRatePerSec,
		AssignmentBurst:       cfg.Runtime.AssignmentRateBurst,
		EndpointRatePerSec:    cfg.Runtime.EndpointRatePerSec,
		EndpointBurst:         cfg.Runtime.EndpointRateBurst,
	}, clock)
	deliveryWorker, err := worker.New(worker.Options{
		Backend:         built,
		Transport:       client,
		Sink:            sink,
		Clock:           clock,
		IDs:             ids,
		Logger:          logger,
		Metrics:         registry,
		Limiter:         limiter,
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

// buildMaintenance constructs the retention/cleanup worker when the backend
// supports durable retention. Ephemeral backends return nil.
func buildMaintenance(
	runtimeCfg config.RuntimeConfig,
	logger *slog.Logger,
	clock runtime.Clock,
	registry *metrics.Registry,
	built runtime.Backend,
) *maintenance.Worker {
	retention, ok := built.(maintenance.RetentionBackend)
	if !ok {
		return nil
	}
	worker, err := maintenance.New(maintenance.Options{
		Backend:         retention,
		Clock:           clock,
		Logger:          logger,
		Metrics:         registry,
		Interval:        runtimeCfg.RetentionInterval,
		BatchLimit:      runtimeCfg.RetentionBatch,
		OutboxRetention: runtimeCfg.OutboxRetention,
	})
	if err != nil {
		logger.Warn("retention worker not started", slog.String("error", err.Error()))
		return nil
	}
	return worker
}

// operatorStatus builds a payload-free status provider for /statusz. It exposes
// build info, the backend selection, and runtime metrics — never bodies,
// payloads, secrets, or identifiers.
func operatorStatus(
	registry *metrics.Registry,
	runtimeCfg config.RuntimeConfig,
	built runtime.Backend,
	durability string,
	info buildinfo.Info,
) func() any {
	payloadBackend := "postgres"
	if runtimeCfg.UsesObjectStore() {
		payloadBackend = "object-store"
	} else if !built.Durable() {
		payloadBackend = "memory"
	}
	keyProvider := runtimeCfg.KeyProvider
	if keyProvider == "" {
		keyProvider = "static"
	}
	return func() any {
		return map[string]any{
			"service":        "hookshipd",
			"version":        info.Version,
			"backend":        built.Name(),
			"durability":     durability,
			"payloadBackend": payloadBackend,
			"keyProvider":    keyProvider,
			"metrics":        registry.Snapshot(),
		}
	}
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
