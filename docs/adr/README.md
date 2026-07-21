# Architecture decision records

These ADRs record the runtime decisions frozen before the delivery handlers and
worker were implemented. They are pre-release decisions for `hook-service`
`v0.x` and make no availability or service-level claims.

- [0001 — Swappable runtime ports](0001-runtime-ports.md)
- [0002 — PostgreSQL is the durable source of truth](0002-postgres-source-of-truth.md)
- [0003 — At-least-once delivery](0003-at-least-once-delivery.md)
- [0004 — Standard Webhooks outbound signing](0004-standard-webhooks-signing.md)
- [0005 — SSRF-safe pinned-address transport](0005-ssrf-safe-transport.md)
- [0006 — Ephemeral memory profile is evaluation-only](0006-memory-evaluation-only.md)
- [0007 — Dispatch relays without dual writes](0007-dispatch-relays-no-dual-write.md)
