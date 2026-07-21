# 0007 — Dispatch relays without dual writes

Status: accepted (pre-release).

## Context

Lower claim latency and partition-scoped ordering are desirable at higher scale,
but a database-plus-queue dual write is unsafe.

## Decision

The `postgres-redis` and `postgres-kafka` profiles publish advisory dispatch
signals derived only from committed PostgreSQL state. A relayer reads claimable
durable jobs and publishes signals to Redis Streams or Kafka; a notifier wakes
the worker for a low-latency claim. PostgreSQL remains authoritative: losing the
relay reverts to polling and loses no work, and a duplicate signal triggers a
harmless idempotent claim. Signals are produced after commit, so there is no
dual write. Kafka keys by endpoint for partition-scoped ordering.

## Consequences

Redis/Kafka are optional latency/ordering optimizations, not the system of
record. A future Kafka-native acceptance ledger is a separate, currently
unsupported design.
