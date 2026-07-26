# Architecture

## Purpose

This repository is HookShip's portable outbound webhook delivery data plane. It
provides a process boundary, a reviewed protocol boundary, an executable
single-cell PostgreSQL durability model, and — in this release — the delivery
runtime: authenticated ingest, durable idempotent acceptance, Standard
Webhooks-compatible signing, an SSRF-safe delivery worker with retries and a
dead-letter queue, payload-free status, and selectable native backend profiles.

## Current components

```text
hookshipctl ──HTTP diagnostics──> hookshipd
                                      │
                                      ├── /healthz
                                      ├── /readyz
                                      └── /statusz

producer ──event_ingest HTTP──> hookshipd (api) ──pgx──> one PostgreSQL 17+ cell
                                hookshipd (worker) ──┤   ├── idempotency + immutable ingest
                                                     │   ├── encrypted payload metadata/ciphertext
                                                     │   ├── immutable endpoint snapshots
                                                     │   ├── delivery + lease-based dispatch jobs
                                                     │   └── immutable attempts + metadata outbox
                                                     └──> Standard Webhooks-signed delivery
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
  retention transitions. Its `Repository` is a thin facade composing focused
  single-responsibility components — idempotency store, acceptance store,
  object-stage store, job queue, outbox queue, nonce ledger, diagnostics, and
  retention manager. The object-stage state machine is split into
  stage-intent (`stage_intent.go`), write/lease claim (`stage_write.go`,
  `stage_lease.go`), supersession (`stage_supersede.go`), finalize/acceptance
  (`stage_finalize.go`, `stage_accept.go`), and cleanup/query (`stage_query.go`);
  the leased-work queues are split into `queue_job.go`, `queue_outbox.go`, and
  the shared `leased.go`/`queue_common.go`. The `postgres` backend wraps it and
  the ingest handler and delivery worker drive it through the runtime ports.

- `internal/runtime` defines the backend-agnostic ports (assignment/credential
  source, nonce ledger, payload sealer, immutable payload object store,
  acceptance store, durable queue with fencing, metadata outbox,
  diagnostics/redrive, clock, and IDs). `internal/capabilities` owns the
  versioned backend capability schema. `internal/idempotency` is the
  dependency-free home of the canonical request fingerprint and stable-header
  identity, shared byte-for-byte by the runtime ports and the PostgreSQL
  durability layer.
- `internal/backend/{memory,postgres,relay,objectstore}` implement the profiles
  and the S3-compatible payload store; the `postgres` backend wraps
  `internal/durability/postgres`. `internal/keyprovider` is the payload
  key-management seam (static default and an AWS-compatible KMS envelope
  provider), and `internal/awsv4` is the shared dependency-free SigV4 signer.
  `internal/ingest`, `internal/worker`, `internal/signing`,
  `internal/transport`, and `internal/outboxsink` implement the protocol
  handlers and delivery loop against the ports.
- `internal/ratelimit`, `internal/maintenance`, and `internal/metrics` provide
  per-assignment/per-endpoint limits with bounded backpressure, the retention
  and cleanup workers, and the payload-free operator metrics exposed on
  `/statusz`.
- `cmd/hookshipd` runs the `api`, `worker`, and `all` modes; `cmd/hookshipctl`
  adds `migrate`, `doctor`, and `backend` operator commands.

Payload ciphertext is stored either in PostgreSQL (the default) or, when
`HOOKSHIP_PAYLOAD_BACKEND=object-store` is selected on a durable profile, in an
S3-compatible object store through the `PayloadStore` port. The port is
orthogonal to the `PayloadSealer`: the sealer produces ciphertext; the store
persists it under a deterministic, payload-free object key while the PostgreSQL
ledger keeps only the object locator, checksum, and key metadata. Cloud KMS is
available through the `internal/keyprovider` seam and is credential-gated.

## Protocol channels

The v1 protocol keeps three channels independent:

```text
private control plane -- toolkit adapter_command --> first-party adapter
producer ------------ event_ingest HTTP ---------> hook-service
hook-service -------- toolkit metadata_ingest ---> private control plane
```

- `adapter_command` is the existing toolkit control plane for adapter
  operations. It is not a producer event submission format.
- `event_ingest` is the producer data plane. It authenticates a scoped
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

The authenticated event-ingest boundary, the outbound delivery worker, and the
S3-compatible object-store payload backend are implemented against this
transaction API. A hosted multi-tenant control plane remains out of scope for
this repository, and this project makes no capacity, availability, or
service-level claims and has not been independently audited.
