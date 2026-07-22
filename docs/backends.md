# Backend profiles and capabilities

HookShip exposes selectable native runtime profiles. Every profile declares a
versioned, machine-readable capability document so operators can see the
tradeoffs, operational requirements, and stable reason codes before selecting
one. Capabilities are owned by `hook-service` under the
`backend_capabilities/v1` schema.

Inspect them:

```sh
hookshipctl backend list
hookshipctl backend describe postgres-kafka
hookshipctl backend validate            # validates the environment selection
hookshipctl backend validate -config hookship.yaml
hookshipd capabilities -output=json
```

## Authoritative contract

The canonical, versioned contract lives under
[`../spec/backend-capabilities/v1/`](../spec/backend-capabilities/v1/): a JSON
Schema (`$id` URN `urn:hookship:backend-capabilities:v1`, schema version
`backend_capabilities/v1`) plus one byte-exact fixture per profile. This is the
authoritative cross-repo contract; `hookshipctl backend describe <profile>`
emits each fixture byte-for-byte, and `hookshipd capabilities -output=json`
emits an envelope whose `profiles` are these documents. Consumers such as
`platform` must copy the schema and fixtures verbatim, pin the exact version,
and record provenance — no sibling source dependency.

Every document has the shape `{ $schema, schemaVersion, kind, profile,
properties, capabilities, bounds, requirements }`. `properties` carries
displayName/summary/evaluationOnly/status/reasonCode/reason plus durability,
consistency, delivery, ordering, retry, and failure behavior; `capabilities`
carries the ack barrier, durable protocol-v1 ack, restart recovery, idempotency,
fencing, replay/DLQ, and multi-process/replica declarations; `bounds` carries
the tested envelope; `requirements` carries backup, external services, and
deployment modes.

## Profiles

| Profile | State/idempotency | Dispatch queue | Payload | Intended use |
| --- | --- | --- | --- | --- |
| `memory` | memory | memory | memory | ephemeral tests/demos (evaluation only) |
| `postgres` | PostgreSQL | PostgreSQL leases | PostgreSQL ciphertext | simplest durable local/VM |
| `postgres-redis` | PostgreSQL | Redis Streams from a transactional outbox | PostgreSQL ciphertext | small/medium low latency |
| `postgres-kafka` | PostgreSQL | Kafka from a transactional outbox | PostgreSQL ciphertext | high-throughput cells |

`kafka-native-ledger` is reserved and explicitly `unsupported`: a Kafka-native
acceptance ledger requires a separate protocol and storage design.

## Capability differences

| Capability | `memory` | `postgres` | `postgres-redis` | `postgres-kafka` |
| --- | --- | --- | --- | --- |
| Durable protocol-v1 acknowledgement | No | Yes | Yes (via PostgreSQL) | Yes (via PostgreSQL) |
| Restart recovery | No | Yes | Yes; Redis rebuilt from outbox | Yes; Kafka/outbox reconcile |
| Ordering | Process-local | Per endpoint | Per endpoint (stream advisory) | Partition-scoped |
| Delayed retries | Timer | Native `run_at` | Native `run_at` + wake | Native `run_at` + relay |
| Multi-replica safe | No | Yes | Yes | Yes |
| Delivery semantics | at-least-once | at-least-once | at-least-once | at-least-once |
| Operational complexity | Minimal | Low | Medium | High |

Delivery is always at-least-once; HookShip never claims exactly-once.

## Rules

- PostgreSQL is the source of truth and the protocol-v1 acceptance barrier in
  every durable profile.
- Redis and Kafka receive only advisory dispatch signals derived from committed
  PostgreSQL state. Losing the relay reverts the worker to PostgreSQL polling;
  duplicated signals trigger harmless idempotent claims. There is no
  database-plus-queue dual write.
- The `memory` profile is evaluation-only. It requires `HOOKSHIP_EVALUATION_MODE`,
  refuses production selection, binds loopback, and marks every ingest response
  `durability: "ephemeral"`. It never claims protocol-v1 durable acceptance.
- The S3-compatible object-store payload backend is **wired and supported** on
  the durable profiles. Selecting `HOOKSHIP_PAYLOAD_BACKEND=object-store` stores
  AES-256-GCM ciphertext in an S3-compatible object store through the fenced
  staging protocol in `internal/durability/postgres`; the PostgreSQL ledger keeps
  only the object locator, checksum, and key metadata, never the bytes. The
  protocol-v1 202 barrier requires a successful immutable object PUT plus the
  durable PostgreSQL acceptance and dispatch-job commit, so a failure never
  yields a false acceptance (a crash may leave a recoverable orphan object that
  the retention worker reclaims). PostgreSQL-byte ciphertext remains the default;
  object-store requires a durable profile and is rejected on the evaluation
  profile with `object_store_requires_durable_profile`. The capability documents
  advertise this via the `objectStorePayloadBackend` capability. Additional
  backends can be added under the same schema; invalid combinations are rejected
  with stable reason codes.

## Reason codes

Validation and status use stable reason codes such as
`evaluation_mode_required`, `production_selection_forbidden`,
`required_external_service_missing`, `non_loopback_plaintext_forbidden`,
`exactly_once_unsupported`, `object_store_payload_supported`,
`object_store_requires_durable_profile`, and
`native_queue_ledger_not_designed`. Codes are part of the compatibility surface
and do not change without a schema version bump.
