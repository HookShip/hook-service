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

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/hookship/hook-service/internal/backend/memory"
	backendpg "github.com/hookship/hook-service/internal/backend/postgres"
	"github.com/hookship/hook-service/internal/capabilities"
	"github.com/hookship/hook-service/internal/config"
	durable "github.com/hookship/hook-service/internal/durability/postgres"
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

	sealer, err := buildSealer(deps.Runtime)
	if err != nil {
		if ownsPool {
			pool.Close()
		}
		return nil, err
	}
	backend, err := backendpg.New(ctx, pool, deps.Assignments, sealer, deps.Clock, ownsPool)
	if err != nil {
		if ownsPool {
			pool.Close()
		}
		return nil, err
	}
	return backend, nil
}

// buildSealer resolves the payload encryption key. Durable profiles require a
// configured key; that requirement is enforced by config validation.
func buildSealer(runtimeCfg config.RuntimeConfig) (runtime.PayloadSealer, error) {
	key := runtimeCfg.PayloadKey()
	if len(key) == 0 {
		if runtimeCfg.EvaluationMode {
			key = make([]byte, payloadcrypto.KeySize)
			if _, err := rand.Read(key); err != nil {
				return nil, fmt.Errorf("generate ephemeral payload key: %w", err)
			}
		} else {
			return nil, errors.New("durable payload encryption requires HOOKSHIP_PAYLOAD_KEY")
		}
	}
	return payloadcrypto.NewSealer(key, "postgres://cell/key/1", "1")
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
