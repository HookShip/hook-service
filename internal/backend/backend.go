// Package backend assembles a concrete runtime.Backend from validated
// configuration and resolved assignments. It selects the native profile
// (memory, postgres, postgres-redis, postgres-kafka) and constructs the payload
// sealer and, for durable profiles, the PostgreSQL pool.
package backend

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"os"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hookship/hook-service/internal/awsv4"
	"github.com/hookship/hook-service/internal/backend/memory"
	"github.com/hookship/hook-service/internal/backend/objectstore"
	backendpg "github.com/hookship/hook-service/internal/backend/postgres"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	durable "github.com/hookship/hook-service/internal/durability/postgres"
	"github.com/hookship/hook-service/internal/keyprovider"
	"github.com/hookship/hook-service/internal/payloadcrypto"
	"github.com/hookship/hook-service/internal/runtime"
)

// Deps carries everything needed to build a backend.
type Deps struct {
	Runtime     config.RuntimeConfig
	Assignments []runtime.Assignment
	Clock       runtime.Clock
	IDs         runtime.IDSource
	Logger      *slog.Logger
	// Pool is an optional pre-built pool for durable profiles. When nil, Build
	// creates and owns one from Runtime.DatabaseURL.
	Pool *pgxpool.Pool
}

// Build constructs the backend selected by the runtime configuration.
func Build(ctx context.Context, deps Deps) (runtime.Backend, error) {
	if deps.Clock == nil {
		deps.Clock = runtime.SystemClock{}
	}
	if deps.IDs == nil {
		deps.IDs = runtime.NewULIDSource(deps.Clock)
	}
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if len(deps.Assignments) == 0 {
		return nil, errors.New("backend requires at least one assignment")
	}

	switch deps.Runtime.Backend {
	case capabilities.ProfileMemory:
		return memory.New(deps.Assignments, deps.Clock, deps.IDs, memory.DefaultLimits())
	case capabilities.ProfilePostgres, capabilities.ProfilePostgresRedis, capabilities.ProfilePostgresKafka:
		return buildPostgres(ctx, deps)
	default:
		return nil, fmt.Errorf("unsupported backend profile %q", deps.Runtime.Backend)
	}
}

func buildPostgres(ctx context.Context, deps Deps) (runtime.Backend, error) {
	pool := deps.Pool
	ownsPool := false
	if pool == nil {
		if deps.Runtime.DatabaseURL == "" {
			return nil, errors.New("durable backend requires HOOKSHIP_DATABASE_URL")
		}
		created, err := pgxpool.New(ctx, deps.Runtime.DatabaseURL)
		if err != nil {
			return nil, fmt.Errorf("connect to PostgreSQL: %w", err)
		}
		pool = created
		ownsPool = true
	}

	sealer, err := buildSealer(ctx, deps.Runtime)
	if err != nil {
		if ownsPool {
			pool.Close()
		}
		return nil, err
	}
	store, err := buildPayloadStore(deps.Runtime, deps.Clock)
	if err != nil {
		if ownsPool {
			pool.Close()
		}
		return nil, err
	}
	options := []backendpg.Option{
		backendpg.WithPayloadStore(store),
		backendpg.WithIDSource(deps.IDs),
	}
	if ownsPool {
		options = append(options, backendpg.WithOwnedPool())
	}
	backend, err := backendpg.New(ctx, backendpg.Params{
		Pool:        pool,
		Assignments: deps.Assignments,
		Sealer:      sealer,
		Clock:       deps.Clock,
	}, options...)
	if err != nil {
		if ownsPool {
			pool.Close()
		}
		return nil, err
	}
	return backend, nil
}

// buildSealer resolves the payload encryption key through the configured key
// provider (static local key by default, or an AWS-compatible KMS envelope
// provider) and builds the AES-256-GCM sealer. Durable profiles require a
// configured key or KMS; that requirement is enforced by config validation.
func buildSealer(ctx context.Context, runtimeCfg config.RuntimeConfig) (runtime.PayloadSealer, error) {
	provider, err := buildKeyProvider(runtimeCfg)
	if err != nil {
		return nil, err
	}
	material, err := provider.Active(ctx)
	if err != nil {
		return nil, fmt.Errorf("resolve payload key from %s provider: %w", provider.Name(), err)
	}
	return keyprovider.NewSealer(material)
}

// buildKeyProvider selects the payload key provider from configuration.
func buildKeyProvider(runtimeCfg config.RuntimeConfig) (keyprovider.Provider, error) {
	if runtimeCfg.UsesKMS() {
		return keyprovider.NewKMS(keyprovider.KMSConfig{
			KeyID:    runtimeCfg.KMS.KeyID,
			Region:   runtimeCfg.KMS.Region,
			Endpoint: runtimeCfg.KMS.Endpoint,
			Credentials: awsv4.Credentials{
				AccessKeyID:     runtimeCfg.KMS.AccessKeyID,
				SecretAccessKey: runtimeCfg.KMS.SecretAccessKey,
				SessionToken:    runtimeCfg.KMS.SessionToken,
			},
			WrappedDataKey: runtimeCfg.KMS.WrappedDataKey,
		})
	}
	key := runtimeCfg.PayloadKey()
	if len(key) == 0 {
		if runtimeCfg.EvaluationMode {
			key = make([]byte, payloadcrypto.KeySize)
			if _, err := rand.Read(key); err != nil {
				return nil, fmt.Errorf("generate ephemeral payload key: %w", err)
			}
		} else {
			return nil, errors.New("durable payload encryption requires HOOKSHIP_PAYLOAD_KEY or HOOKSHIP_KEY_PROVIDER=aws-kms")
		}
	}
	return keyprovider.NewStatic(key, "")
}

// buildPayloadStore constructs the S3-compatible object-store PayloadStore when
// the object-store payload backend is selected; otherwise it returns nil so the
// PostgreSQL-byte payload store remains the default.
func buildPayloadStore(runtimeCfg config.RuntimeConfig, clock runtime.Clock) (runtime.PayloadStore, error) {
	if !runtimeCfg.UsesObjectStore() {
		return nil, nil
	}
	settings := runtimeCfg.ObjectStore
	var caBundle []byte
	if settings.CABundlePath != "" {
		bundle, err := os.ReadFile(settings.CABundlePath)
		if err != nil {
			return nil, fmt.Errorf("read object-store CA bundle: %w", err)
		}
		caBundle = bundle
	}
	return objectstore.New(objectstore.Config{
		Endpoint:    settings.Endpoint,
		Region:      settings.Region,
		Bucket:      settings.Bucket,
		PathStyle:   settings.PathStyle,
		UseTLS:      settings.UseTLS,
		CABundlePEM: caBundle,
		Credentials: awsv4.Credentials{
			AccessKeyID:     settings.AccessKeyID,
			SecretAccessKey: settings.SecretAccessKey,
			SessionToken:    settings.SessionToken,
		},
		Timeout: runtimeCfg.DeliveryTimeout,
		Clock:   clock,
	})
}

// Migrate applies the durability migrations for durable profiles. It is a no-op
// for the ephemeral memory profile.
func Migrate(ctx context.Context, runtimeCfg config.RuntimeConfig) error {
	document, ok := capabilities.Lookup(runtimeCfg.Backend)
	if !ok {
		return fmt.Errorf("unknown backend %q", runtimeCfg.Backend)
	}
	if document.Properties.Durability == capabilities.DurabilityEphemeral {
		return nil
	}
	if runtimeCfg.DatabaseURL == "" {
		return errors.New("migration requires HOOKSHIP_DATABASE_URL")
	}
	pool, err := pgxpool.New(ctx, runtimeCfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("connect to PostgreSQL: %w", err)
	}
	defer pool.Close()
	return durable.Migrate(ctx, pool)
}
