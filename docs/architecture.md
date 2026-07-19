# Architecture

## Purpose

This repository is intended to become HookShip's portable outbound webhook
delivery data plane. It currently provides a process boundary, reviewed
protocol boundary, and executable single-cell PostgreSQL durability model;
public ingest and delivery runtime work remain unimplemented.

## Current components

```text
hookshipctl ──HTTP diagnostics──> hookshipd
                                      │
                                      ├── /healthz
                                      └── /readyz

future ingest/runtime ──pgx──> one PostgreSQL 17+ regional cell
                               ├── idempotency + immutable ingest
                               ├── encrypted payload metadata/ciphertext
                               ├── immutable endpoint snapshots
                               ├── delivery + lease-based dispatch jobs
                               └── immutable attempts + metadata outbox
```

- `cmd/hookshipd` owns process startup, signal handling, configuration, and
  structured logging.
- `internal/httpserver` owns bounded HTTP server behavior, operational probes,
  and graceful shutdown.
- `cmd/hookshipctl` and `internal/diagnostics` provide bounded local
  diagnostics and build metadata.
- `internal/buildinfo` centralizes metadata injected at build time.
- `internal/durability/postgres` owns forward-only migrations, migration
  checksum enforcement, assignment-scoped persistence, atomic acceptance,
  restart-readable object staging metadata, database-clock leases, fencing,
  versioned stage-write attempt identities, payload-deletion recovery, and
  retention transitions. It is not wired into `hookshipd` yet.

There is intentionally no delivery request handler, outbound worker, signing
runtime, control-plane client, object-store provider, or cloud integration.
PostgreSQL jobs and outbox rows are the durable scheduling primitives, not an
active worker implementation.

## Protocol channels

The v1 protocol keeps three channels independent:

```text
private control plane -- toolkit adapter_command --> first-party adapter
producer ------------ event_ingest HTTP ---------> hook-service (future)
hook-service -------- toolkit metadata_ingest ---> private control plane
```

- `adapter_command` is the existing toolkit control plane for adapter
  operations. It is not a producer event submission format.
- `event_ingest` is the new producer data plane. It authenticates a scoped
  assignment, preserves the exact HTTP entity body, and acknowledges only
  after the v1 durability barrier.
- `metadata_ingest` is the existing toolkit channel for canonical,
  payload-free delivery observations. It never transports producer bodies or
  arbitrary headers.

The normative contract is under [`../spec/v1/`](../spec/v1/). The private
first-party adapter mapping is documented without importing private code in
[`../spec/v1/integration.md`](../spec/v1/integration.md).

## Operational boundaries

- The default listener is loopback-only.
- Public exposure requires explicit operator configuration and an appropriate
  deployment security boundary.
- HTTP read, header, write, idle, and shutdown periods are bounded.
- Probe responses never include request data.
- The process does not log HTTP request bodies or webhook payloads.
- Readiness becomes false before graceful shutdown begins.

## Durability boundary

The v1 persistence decision is documented in
[`durability.md`](durability.md). Assignment-scoped composite foreign keys
prevent cross-assignment lineage, and a deferred commit-barrier trigger
requires committed payload, immutable ingest, delivery, and ready dispatch
job state before an idempotency ledger can commit.

The next phase may implement the authenticated event-ingest boundary against
this transaction API. Outbound delivery workers, runtime control-plane
endpoints, and cloud integration still require separate review.
