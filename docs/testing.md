# Testing and coverage

hook-service is verified at three levels, each wired into `make check`/CI:

- **Unit tests** (`make test`, `make test-race`) run without external services.
  They cover pure decision logic (idempotency fingerprints, error translation,
  durable↔runtime conversion parity, state transitions, key derivation, time
  normalization, config parsing/validation, signing canonical content) and the
  in-memory backend contract.
- **Architecture-conformance tests** (`internal/archtest`) run in the unit lane:
  an import-graph guard (core packages stay free of concrete backend/config
  imports) and a source-size ratchet for non-generated Go and shell files.
- **Integration tests** (`make test-integration`, build tag `integration`)
  require real PostgreSQL 17, and optionally Redis, Kafka, and MinIO. They run
  the shared backend contract against the durable profile and exercise the
  object-store payload path, fencing, retention, and recovery.

## Shared backend contract

`internal/backend/backendtest` defines one contract suite that both the memory
backend (unit) and the PostgreSQL backend (integration) must satisfy, so the two
profiles are held to identical functional behavior. It covers acceptance and
status, duplicate/conflict handling, the delivery lifecycle, fencing, DLQ and
redrive, nonce replay, the metadata outbox, lease-expiry reclaim, exact
stale-fence rejection, and the exhaustion boundary for both the job and outbox
queues (`contract.go`, `contract_queue.go`).

## Running integration tests

```sh
export TEST_DATABASE_URL='postgres://hookship@127.0.0.1:5432/hookship_test?sslmode=disable'
# Optional broker/object-store integration:
export REDIS_URL='redis://127.0.0.1:6379/0'
export KAFKA_BROKERS='127.0.0.1:9092'
export MINIO_TEST_ENDPOINT='127.0.0.1:9000'
export MINIO_TEST_BUCKET='hookship-payloads'
export MINIO_TEST_ACCESS_KEY='...' MINIO_TEST_SECRET_KEY='...' MINIO_TEST_REGION='us-east-1'

make migration-check test-integration
```

The disposable recovery drills run separately (they need docker and PostgreSQL
client tools):

```sh
make smoke-deploy   # real restore drill + real Compose network derivation
```

## Coverage floors (enforced)

These floors are enforced by `make coverage` (`scripts/coverage.sh`), which runs
in CI: the unit floors in every `verify` run, and the integration floors in the
PostgreSQL/Redis/MinIO integration job (and locally whenever `TEST_DATABASE_URL`
is set). The gate fails on any regression below a floor. Floors are a ratchet:
raise them as coverage rises, never lower one to make a regression pass.

```sh
make coverage                       # unit floors
TEST_DATABASE_URL=... MINIO_TEST_...=... make coverage   # + integration floors
```

| Package | Lane | Floor |
| --- | --- | --- |
| `internal/idempotency` | unit | 99% |
| `internal/capabilities` | unit | 97% |
| `internal/signing` | unit | 88% |
| `internal/ratelimit` | unit | 88% |
| `internal/worker` | unit | 82% |
| `internal/transport` | unit | 81% |
| `internal/runtime` | unit | 75% |
| `internal/config` | unit | 73% |
| `internal/ingest` | unit | 72% |
| `internal/backend/postgres` | unit | 19% |
| `internal/durability/postgres` | unit | 18% |
| `internal/durability/postgres` | integration | 56% |
| `internal/backend/postgres` | integration | 73% |

`internal/durability/postgres` and `internal/backend/postgres` are database
wrappers. Their pure decision logic (fingerprints, state transitions, key
derivation, time normalization, validation, error/lease classification, scans,
and conversions) is unit-tested here — roughly doubling their unit coverage from
~10% — but their SQL orchestration is only exercisable against a real database,
so the meaningful coverage for those two packages is the integration lane
(56%/73%). A 30% unit floor is not achievable without a live database, which is
why the SQL paths are covered by the integration floors instead.

Measure a package's integration coverage with:

```sh
go test -count=1 -tags=integration -cover ./internal/durability/postgres
```
