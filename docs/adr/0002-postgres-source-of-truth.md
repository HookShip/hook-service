# 0002 — PostgreSQL is the durable source of truth

Status: accepted (pre-release).

## Context

The first release needs a durable, single-cell acceptance and dispatch store
without operating a broker as the system of record.

## Decision

For every durable profile, PostgreSQL 17+ is the source of truth and the
protocol-v1 acceptance barrier. `AcceptPostgres` commits the immutable ingest,
idempotency ledger, exact encrypted payload, and ready dispatch job atomically.
Dispatch jobs and the metadata outbox are claimed under database-clock fencing
leases with `SKIP LOCKED`. Keep PostgreSQL jobs until measured queue lag or
claim contention justifies more cells or a relay; never introduce a
database-plus-queue dual write.

## Consequences

Restart recovery, multi-replica safety, and durable idempotency come from the
database. A replay-protection nonce ledger was added as an additive forward
migration. The control plane is never required on the delivery hot path.
