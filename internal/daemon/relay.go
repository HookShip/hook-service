package daemon

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/hookship/hook-service/internal/backend/relay"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	"github.com/hookship/hook-service/internal/runtime"
)

// readyDispatchSource is implemented by durable backends that can list claimable
// dispatch jobs for the relay.
type readyDispatchSource interface {
	ReadyDispatch(ctx context.Context, limit int) ([]runtime.Job, error)
}

// relayComponents bundles the running relay for a profile.
type relayComponents struct {
	wake     chan struct{}
	relayer  *relay.Relayer
	notifier *relay.Notifier
	closers  []func() error
}

// launch registers the relay's best-effort goroutines with the shared launcher
// so they are joined on shutdown. Relay failure is auxiliary: it is logged and
// never tears down the worker, because PostgreSQL is the source of truth and the
// worker falls back to polling.
func (c *relayComponents) launch(launchAux func(name string, run func(context.Context) error)) {
	if c == nil {
		return
	}
	launchAux("dispatch relayer", c.relayer.Run)
	launchAux("dispatch notifier", c.notifier.Run)
}

func (c *relayComponents) close() {
	if c == nil {
		return
	}
	for _, closer := range c.closers {
		_ = closer()
	}
}

// buildRelay constructs the dispatch relay for postgres-redis and postgres-kafka
// profiles. It returns nil for profiles that do not use a relay.
func buildRelay(ctx context.Context, runtimeCfg config.RuntimeConfig, built runtime.Backend, logger *slog.Logger) (*relayComponents, error) {
	source, ok := built.(readyDispatchSource)
	if !ok {
		return nil, nil
	}
	consumerName := hostConsumerName()

	var publisher relay.Publisher
	var consumer relay.Consumer
	var err error
	switch runtimeCfg.Backend {
	case capabilities.ProfilePostgresRedis:
		redisConfig := relay.RedisConfig{URL: runtimeCfg.RedisURL, Consumer: consumerName}
		publisher, err = relay.NewRedisPublisher(redisConfig)
		if err != nil {
			return nil, fmt.Errorf("build redis publisher: %w", err)
		}
		consumer, err = relay.NewRedisConsumer(ctx, redisConfig)
		if err != nil {
			_ = publisher.Close()
			return nil, fmt.Errorf("build redis consumer: %w", err)
		}
	case capabilities.ProfilePostgresKafka:
		kafkaConfig := relay.KafkaConfig{Brokers: runtimeCfg.KafkaBrokers}
		publisher, err = relay.NewKafkaPublisher(kafkaConfig)
		if err != nil {
			return nil, fmt.Errorf("build kafka publisher: %w", err)
		}
		consumer, err = relay.NewKafkaConsumer(kafkaConfig)
		if err != nil {
			_ = publisher.Close()
			return nil, fmt.Errorf("build kafka consumer: %w", err)
		}
	default:
		return nil, nil
	}

	wake := make(chan struct{}, 1)
	relayer := relay.NewRelayer(relay.RelayerOptions{
		Source:    signalSource(source),
		Publisher: publisher,
		Logger:    logger,
	})
	notifier := relay.NewNotifier(relay.NotifierOptions{Consumer: consumer, Wake: wake, Logger: logger})
	return &relayComponents{
		wake:     wake,
		relayer:  relayer,
		notifier: notifier,
		closers:  []func() error{publisher.Close, consumer.Close},
	}, nil
}

// signalSource adapts a ready-dispatch backend into a relay signal source.
func signalSource(source readyDispatchSource) relay.SignalSource {
	return func(ctx context.Context, limit int) ([]relay.Signal, error) {
		jobs, err := source.ReadyDispatch(ctx, limit)
		if err != nil {
			return nil, err
		}
		signals := make([]relay.Signal, 0, len(jobs))
		for _, job := range jobs {
			signals = append(signals, relay.NewSignal(job.AssignmentID, job.JobID, job.EndpointSnapshotID, job.RunAt.UnixMilli()))
		}
		return signals, nil
	}
}

func hostConsumerName() string {
	return "hookship-worker-" + runtime.NewULIDSource(runtime.SystemClock{}).NewID("c")
}
