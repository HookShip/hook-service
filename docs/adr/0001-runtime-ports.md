# 0001 — Swappable runtime ports

Status: accepted (pre-release).

## Context

The delivery core must run identically across an ephemeral evaluation backend
and durable PostgreSQL-backed profiles without leaking backend details into the
ingest handler or worker.

## Decision

Define backend-agnostic ports in `internal/runtime`: assignment/credential
source, nonce ledger, payload sealer, idempotency/acceptance store, durable
queue with fencing leases, metadata outbox, diagnostics/redrive, plus clock and
ID sources. A `Backend` interface bundles them. The ingest handler and worker
depend only on these ports. `internal/runtime` imports no concrete backend, so
the same canonical identifiers and state transitions hold across profiles.

## Consequences

Memory and PostgreSQL backends are held to one shared contract suite. New
backends (relays, future object/KMS/SQS combinations) implement the same ports.
Protocol v1 remains backward compatible; an incompatible wire change requires a
new major protocol version.
