# 0003 — At-least-once delivery

Status: accepted (pre-release).

## Context

A crash after a provider side effect but before the job settles must not drop
or silently duplicate-hide a delivery.

## Decision

Delivery is at-least-once. A claimed job increments its attempt under a fencing
lease; a stale owner cannot settle a reclaimed job. Ambiguous failures are
retried, not treated as delivered. Acceptance guarantees durability, not
exactly-once processing. The capability schema rejects any request for
exactly-once with the `exactly_once_unsupported` reason code.

## Consequences

Downstream consumers must remain idempotent. The delivery message id is stable
across retries so receivers can deduplicate.
