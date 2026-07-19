# Architecture

## Purpose

This repository is intended to become HookShip's portable outbound webhook
delivery data plane. It currently provides a production-quality process
boundary and a reviewed protocol boundary; delivery runtime work remains
unimplemented.

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

## Next gated phase

The next phase may implement the accepted ingest boundary and its durability
model. Delivery workers, databases, runtime control-plane endpoints, and cloud
integration are outside this specification change and require their own
review. The gate and current status live in
[`../spec/README.md`](../spec/README.md).
