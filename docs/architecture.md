# Architecture

## Purpose

This repository is intended to become HookShip's portable outbound webhook
delivery data plane. The initial phase establishes only a production-quality
process boundary and the engineering controls required for later delivery
work.

## Current components

```text
hookshipctl ──HTTP diagnostics──> hookshipd
                                      │
                                      ├── /healthz
                                      └── /readyz
```

- `cmd/hookshipd` owns process startup, signal handling, configuration, and
  structured logging.
- `internal/httpserver` owns bounded HTTP server behavior, operational probes,
  and graceful shutdown.
- `cmd/hookshipctl` and `internal/diagnostics` provide bounded local
  diagnostics and build metadata.
- `internal/buildinfo` centralizes metadata injected at build time.

There is intentionally no delivery request handler, work scheduler, retry
engine, queue, persistence adapter, signing mechanism, tenant model, or cloud
integration.

## Operational boundaries

- The default listener is loopback-only.
- Public exposure requires explicit operator configuration and an appropriate
  deployment security boundary.
- HTTP read, header, write, idle, and shutdown periods are bounded.
- Probe responses never include request data.
- The process does not log HTTP request bodies or webhook payloads.
- Readiness becomes false before graceful shutdown begins.

## Next gated phase

Protocol and durability semantics must be specified and reviewed before
delivery implementation starts. The gate must establish the behavior and
failure boundaries necessary for interoperability without assuming an API in
advance. The placeholder and review rules live in [`../spec/README.md`](../spec/README.md).
