# Compatibility matrix

This repository owns the outbound delivery **protocol** and the runtime
**capability surface** it exposes; both are the source of truth here (see the
organization
[source-of-truth rules](https://github.com/HookShip/.github/blob/main/SOURCE_OF_TRUTH.md)).
The contract and signing primitives the protocol builds on are owned by
[`toolkit`](https://github.com/HookShip/toolkit) and referenced, not restated.

`hook-service` is pre-release (`v0.x`); nothing below is a release claim.

## Protocol and capability contracts

| Contract | Version | Source of truth |
| --- | --- | --- |
| Delivery protocol | `v1` | [`spec/v1/`](../spec/v1/) |
| Backend capability schema | `backend_capabilities/v1` | [`spec/backend-capabilities/v1/`](../spec/backend-capabilities/v1/) |
| Outbound signature | Standard Webhooks-compatible | [`spec/v1/signing.md`](../spec/v1/signing.md) |
| Error responses | RFC 9457 `application/problem+json` | [`spec/v1/`](../spec/v1/) |

## Backend profiles

Values come from the machine-readable capability documents
(`hookshipctl backend list -output json`). Delivery is **at-least-once** on every
profile; downstream idempotency remains necessary.

| Profile | Status | Durability | Delivery | Ordering | Requires |
| --- | --- | --- | --- | --- | --- |
| `memory` | supported | ephemeral | at-least-once | none | none (evaluation only) |
| `postgres` | supported | disk | at-least-once | per-endpoint | PostgreSQL 17+ |
| `postgres-redis` | supported | replicated | at-least-once | per-endpoint | PostgreSQL 17+, Redis |
| `postgres-kafka` | supported | replicated | at-least-once | per-partition | PostgreSQL 17+, Kafka |
| `kafka-native-ledger` | unsupported | — | — | — | reserved; not designed |

PostgreSQL is the source of truth and acceptance barrier in every durable
profile; Redis/Kafka carry only advisory dispatch signals derived from committed
state (no dual write). See [`backends.md`](backends.md).

## Payload storage and key providers

| Payload backend | Stores | Requires |
| --- | --- | --- |
| `postgres` (default) | AES-256-GCM ciphertext in PostgreSQL | a durable profile |
| `object-store` | AES-256-GCM ciphertext in an S3-compatible store | a durable profile + `HOOKSHIP_PAYLOAD_OBJECT_*` |

| Key provider | Rotation | Requires |
| --- | --- | --- |
| `static` (default) | constrained (single active key) — see [key rotation](runbooks/key-rotation.md) | a 32-byte key |
| `aws-kms` | zero-downtime rewrap of the same DEK | `HOOKSHIP_KMS_*` |

## Runtime and build

| Component | Version / target |
| --- | --- |
| Go toolchain | 1.26.x (module `go 1.26.0`, toolchain `go1.26.5`) |
| PostgreSQL | 17+ (server version enforced by the migrator) |
| Redis / Kafka / MinIO | exercised in CI at the digests pinned in [`compose.yaml`](../compose.yaml) and [`.github/workflows/ci.yml`](../.github/workflows/ci.yml) |
| Release archives | `linux/amd64`, `linux/arm64`, `darwin/amd64`, `darwin/arm64` |
| Container image | `linux/amd64`, `linux/arm64` (multi-arch) |

Concrete third-party versions are pinned by digest in the Dockerfile, Compose
files, and workflows rather than asserted as compatibility guarantees here.
