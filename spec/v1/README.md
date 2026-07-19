# HookShip protocol v1

## Status and scope

This directory is the reviewed source-of-truth protocol for the first
`hook-service` interoperability boundary. It specifies:

- producer raw-body `event_ingest` HTTP requests and payload-free status;
- mandatory non-loopback HTTPS with TLS 1.2 minimum and TLS 1.3 preferred;
- HMAC-SHA256 authentication, replay resistance, and idempotency;
- bounded RFC 9457 errors and retry behavior;
- compatibility and independent toolkit integration;
- minimal future configuration-bundle and fleet-registration interfaces.

It does not implement delivery, persistence, control-plane APIs, databases,
fleet management, or cloud-provider integration.

## Channel separation

| Channel | Owner and purpose | Payload rule | Version |
| --- | --- | --- | --- |
| `adapter_command` | Existing toolkit control-plane adapter operations | Command JSON only; never producer event bodies | Toolkit schema `2026-07-01` |
| `event_ingest` | New producer-to-service data plane | Exact raw HTTP entity body | HookShip HTTP protocol `1` |
| `metadata_ingest` | Existing toolkit delivery observations | Canonical allowlisted metadata only; no raw body or arbitrary headers | Toolkit schema `2026-07-01` |

The channels are not interchangeable. In particular, a producer event is not a
`metadata_ingest` record.

## Artifacts

- [`protocol.md`](protocol.md): normative ingest, durability, idempotency,
  status, errors, and retries.
- [`signing.md`](signing.md): canonical request signing and replay rules.
- [`openapi.json`](openapi.json): OpenAPI 3.1 HTTP contract.
- [`compatibility.md`](compatibility.md): versioning and change policy.
- [`configuration.md`](configuration.md): future signed configuration and fleet
  boundaries.
- [`integration.md`](integration.md): private first-party adapter mapping.
- [`schemas/`](schemas/): versioned JSON Schemas.
- [`schema-registry.json`](schema-registry.json): absolute `$id` to local
  resource registry for Draft 2020-12 validators.
- [`fixtures/`](fixtures/): golden HTTP outcomes, timestamp cases, fleet
  reports, and deterministic HMAC/Ed25519 vectors.
- [`checksums.txt`](checksums.txt): deterministic SHA-256 manifest.

Normative words **MUST**, **MUST NOT**, **SHOULD**, and **MAY** are used as
defined by RFC 2119 and RFC 8174.
