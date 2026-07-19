# HookShip delivery service

`hook-service` is the portable outbound webhook delivery data-plane foundation
for HookShip. This repository establishes the process, operational, build, and
protocol boundaries needed before delivery semantics are implemented.

> **Status:** foundation plus a reviewed v1 protocol specification. There is no
> webhook delivery implementation, queue, durability layer, database, signing
> runtime, cloud integration, or delivery worker in this repository yet.

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

The operational endpoints are:

- `GET` or `HEAD /healthz`
- `GET` or `HEAD /readyz`

They are process-level probes, not a webhook delivery API.

## Quick start

Requirements: Go 1.26.5 or newer.

```sh
make check
./bin/hookshipd
./bin/hookshipctl diagnostics
```

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

- Standard library first; the initial module has no third-party Go
  dependencies.
- No request or payload logging.
- No implicit public network exposure.
- No runtime delivery or persistence behavior before its separately reviewed
  implementation phase.
- No availability, throughput, or service-level claims.

The protocol is specified, but runtime durability and delivery remain gated.
See [`spec/README.md`](spec/README.md), [`spec/v1/`](spec/v1/), and
[`docs/architecture.md`](docs/architecture.md).

## Documentation

- [Configuration](docs/configuration.md)
- [Operations](docs/operations.md)
- [Architecture](docs/architecture.md)
- [Development](docs/development.md)
- [Provenance](docs/provenance.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
