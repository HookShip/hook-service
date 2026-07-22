# HookShip delivery service

`hook-service` is the portable outbound webhook delivery data-plane foundation
for HookShip. This repository establishes the process, operational, protocol,
and single-cell durability boundaries needed before delivery execution.

> **Status:** pre-release (`v0.x`). This repository now implements the outbound
> delivery runtime: protocol-v1 authenticated ingest, idempotent durable
> acceptance, Standard Webhooks-compatible signing, an SSRF-safe delivery
> worker with retries/DLQ, payload-free status, selectable native backend
> profiles (`memory`, `postgres`, `postgres-redis`, `postgres-kafka`), an
> S3-compatible object-store payload backend, a static/AWS-KMS payload key
> provider seam, and per-assignment/per-endpoint limits with retention workers
> and payload-free operator metrics. It has not been released or independently
> audited, and it makes no availability, throughput, or service-level claims.
> PostgreSQL is the source of truth for the durable profiles; delivery is
> at-least-once (never exactly-once).

## Surface

- `hookshipd`: one binary with explicit modes — `api` (ingest and status),
  `worker` (dispatch, retry, DLQ, metadata outbox), and `all` (single process).
  It also prints machine-readable backend capabilities.
- `hookshipctl`: `version`, `diagnostics`, `doctor`, `migrate`, and
  `backend list|describe|validate`.
- Protocol-v1 producer data plane: exact-body HMAC authentication, nonce replay
  protection, assignment authorization, idempotent acceptance across the
  durability barrier, and payload-free `GET` status, all returning bounded
  RFC 9457 problem responses.
- SSRF-safe delivery worker: DNS pinning, private/link-local/metadata rejection,
  redirect refusal, TLS verification, bounded timeouts and response discard,
  Standard Webhooks signing over exact payload bytes, retry classification with
  exponential backoff and jitter, circuit breaking, dead-letter queue, and
  explicit operator redrive.
- Selectable native backend profiles with a versioned, machine-readable
  capability schema and stable reason codes. See
  [`docs/backends.md`](docs/backends.md).
- `spec/v1`: the source-of-truth contract for producer event ingestion,
  payload-free status, inbound signing, errors, and compatibility.
- `internal/durability/postgres`: forward-only PostgreSQL 17+ migrations and a
  narrow pgx v5 repository for the durable profiles.

Operational probes remain `GET`/`HEAD /healthz` and `GET`/`HEAD /readyz`;
readiness reflects backend health and reports the profile durability.

## Quick start

Requirements: Go 1.26.5 or newer. PostgreSQL is optional for ordinary checks.

```sh
make check
./bin/hookshipd
./bin/hookshipctl diagnostics
```

Run durability integration tests against a disposable PostgreSQL 17+ database:

```sh
TEST_DATABASE_URL='postgres://hookship@127.0.0.1:5432/hookship_test?sslmode=disable' \
  make test-integration
```

Explicitly invoking `make test-integration` without `TEST_DATABASE_URL` fails.
`make check` remains database- and Docker-independent.

The default listener is `127.0.0.1:8080`. An external bind must be an explicit
configuration choice:

```sh
HOOKSHIP_LISTEN_ADDRESS=0.0.0.0:8080 ./bin/hookshipd
```

The optional Compose profiles run the delivery vertical slice locally. One
command demonstrates accept -> signed delivery -> retry/DLQ -> status:

```sh
# Ephemeral evaluation profile (memory backend, no PostgreSQL):
docker compose --profile memory up --build

# Durable evaluation profile (PostgreSQL + migration + api + worker + sink):
docker compose --profile evaluation up --build

# Durable profile with S3-compatible object-store ciphertext payloads (MinIO):
docker compose --profile object-store up --build

# Or run the scripted demo (memory|evaluation|redis|kafka|object-store; add
# "retry" to exercise retry/DLQ):
scripts/demo.sh memory
scripts/demo.sh evaluation retry
scripts/demo.sh object-store
```

The `redis` and `kafka` profiles add their real services and a transactional
dispatch relay. The `object-store` profile adds MinIO and stores payload
ciphertext there. PostgreSQL remains the source of truth and the acceptance
barrier in every durable profile.

The image does not bake in a fixed-port healthcheck. Deployments that override
the listener must define a matching probe.

## Design boundaries

- Standard library first; direct Go dependencies are pinned and verified
  (`pgx/v5`, `yaml.v3`, and the Redis/Kafka clients used only by their profiles).
  The S3-compatible object store and AWS KMS provider speak their REST/JSON APIs
  directly over a dependency-free AWS SigV4 signer, so no cloud SDK is added.
- No request or payload logging; attempts, outbox rows, status, metadata, and
  the `/statusz` operator metrics are payload-free.
- No implicit public network exposure; non-loopback plaintext fails closed
  unless an operator asserts an explicit proxy boundary.
- PostgreSQL is the durable queue and source of truth; Redis/Kafka carry only
  advisory dispatch signals derived from committed state, so there is no
  database-plus-queue dual write.
- Stored payload bytes are AES-256-GCM ciphertext only, in PostgreSQL by default
  or in an S3-compatible object store; the object-store 202 barrier requires the
  immutable object PUT plus the durable PostgreSQL commit, and the database keeps
  only object locators, hashes, and key metadata — never raw bytes.
- At-least-once delivery; downstream idempotency remains necessary.
- No availability, throughput, or service-level claims; no invented domains,
  registries, or contacts.

See [`docs/backends.md`](docs/backends.md), [`docs/durability.md`](docs/durability.md),
[`spec/README.md`](spec/README.md), [`spec/v1/`](spec/v1/), and
[`docs/architecture.md`](docs/architecture.md).

## Documentation

- [Configuration](docs/configuration.md)
- [Operations](docs/operations.md)
- [Architecture](docs/architecture.md)
- [Backend profiles and capabilities](docs/backends.md)
- [Durability model](docs/durability.md)
- [Architecture decision records](docs/adr/)
- [Development](docs/development.md)
- [Provenance](docs/provenance.md)
- [Security policy](SECURITY.md)
- [Contributing](CONTRIBUTING.md)

## License

Apache License 2.0. See [LICENSE](LICENSE), [NOTICE](NOTICE), and
[THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md).
