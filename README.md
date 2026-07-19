# HookShip delivery service

`hook-service` is the portable outbound webhook delivery data-plane foundation
for HookShip. This repository establishes the process, operational, protocol,
and single-cell durability boundaries needed before delivery execution.

> **Status:** foundation, reviewed protocol v1, and an executable PostgreSQL
> durability library. There is no public event-ingest handler, signing runtime,
> outbound delivery worker, control-plane API, or cloud-provider integration.

## Current surface

- `hookshipd`: a minimal HTTP process with bounded server timeouts, validated
  environment configuration, structured logs, health/readiness endpoints, and
  graceful shutdown.
- `hookshipctl`: local version reporting and bounded health/readiness
  diagnostics.
- Reproducible build metadata, tests, container packaging, CI, and public
  project documentation.
- `spec/v1`: the source-of-truth contract for producer event ingestion,
  payload-free status, signing, errors, compatibility, and future
  configuration/fleet boundaries.
- `internal/durability/postgres`: forward-only PostgreSQL 17+ migrations and a
  narrow pgx v5 repository for idempotency, encrypted payload metadata,
  immutable delivery configuration, dispatch jobs, attempts, and metadata
  outbox leases. Lease ownership and payload deletion fencing use the
  PostgreSQL clock; object writes use versioned attempt identities and fenced
  conditional finalization.

The operational endpoints are:

- `GET` or `HEAD /healthz`
- `GET` or `HEAD /readyz`

They are process-level probes, not a webhook delivery API.

## Quick start

Requirements: Go 1.26.5 or newer. PostgreSQL is optional for ordinary checks.

```sh
make check
./bin/hookshipd
./bin/hookshipctl diagnostics
```

Run durability integration tests against a disposable PostgreSQL 17+ database:

```sh
TEST_DATABASE_URL='postgres://hookship@127.0.0.1:5432/hookship_test?sslmode=disable' \
  make test-integration
```

Explicitly invoking `make test-integration` without `TEST_DATABASE_URL` fails.
`make check` remains database- and Docker-independent.

The default listener is `127.0.0.1:8080`. An external bind must be an explicit
configuration choice:

```sh
HOOKSHIP_LISTEN_ADDRESS=0.0.0.0:8080 ./bin/hookshipd
```

The optional Compose evaluation profile binds the published port to the host
loopback and defines a deployment-specific healthcheck:

```sh
docker compose --profile evaluation up --build
```

The image does not bake in a fixed-port healthcheck. Deployments that override
the listener must define a matching probe.

## Design boundaries

- Standard library first; pgx v5 is the only direct Go dependency.
- No request or payload logging.
- No implicit public network exposure.
- PostgreSQL is the durable queue; there is no database-plus-broker dual write.
- Stored payload bytes are ciphertext only.
- No public ingest or delivery runtime before its separately reviewed phase.
- No availability, throughput, or service-level claims.

See [`docs/durability.md`](docs/durability.md), [`spec/README.md`](spec/README.md),
[`spec/v1/`](spec/v1/), and [`docs/architecture.md`](docs/architecture.md).

## Documentation

- [Configuration](docs/configuration.md)
- [Operations](docs/operations.md)
- [Architecture](docs/architecture.md)
- [Durability model](docs/durability.md)
- [Development](docs/development.md)
- [Provenance](docs/provenance.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
