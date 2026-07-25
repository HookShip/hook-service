# Development

## Requirements

- Go 1.26.5 or newer
- GNU Make or a compatible `make`
- `shellcheck` for linting the operational shell scripts (`make shellcheck`)
- PostgreSQL 17+ only for durability integration tests
- Docker only for container validation or a disposable local PostgreSQL
- Python 3 with the pinned `requirements-spec.txt` packages when running the
  independent Draft 2020-12 validator locally

## Commands

```sh
make fmt          # format Go source
make fmt-check    # fail if source is not formatted
make test         # unit tests
make test-race    # unit tests with the race detector
make vet          # static checks from go vet
make build        # build bin/hookshipd and bin/hookshipctl
make validate     # repository structure validation
make spec-validate # validate v1 JSON, IDs, checksums, HMAC, and Ed25519
make migration-check # validate immutable embedded migration checksums
make shellcheck   # lint every shell script with `shellcheck -x`
make test-scripts # hermetic tests for the operational recovery scripts
make check        # all local validation (includes shellcheck + test-scripts)
make gitleaks     # require and run the local gitleaks binary
```

The operational recovery scripts under `deploy/scripts/` are safety-critical, so
`make check` lints them with `shellcheck` and runs hermetic shell tests
(`deploy/scripts/test/`) that cover the pure `lib.sh` helpers and the
`rollback.sh` network-derivation and migrate-probe classification (using a fake
`docker` on `PATH`, so no daemon is required). The real disposable-Postgres and
Compose smoke drills need docker and matching PostgreSQL client tools and run
separately:

```sh
make smoke-deploy # real restore drill + real Compose network derivation
```

Two architecture-conformance tests run in the ordinary lane
(`internal/archtest`): an import-graph guard keeps `internal/runtime`,
`internal/idempotency`, and `internal/capabilities` free of concrete
backend/config imports, and a source-size ratchet caps non-generated Go and
shell files (current hotspots are grandfathered with a ceiling that may only be
lowered, never raised, as files are split).

See [testing.md](testing.md) for the three test lanes, the shared backend
contract, and the documented coverage floors.

Database integration tests are intentionally separate:

```sh
export TEST_DATABASE_URL='postgres://hookship@127.0.0.1:5432/hookship_test?sslmode=disable'
make test-integration
```

The target fails when the variable is absent. Tests create and drop isolated
schemas, apply migrations from zero, and require PostgreSQL 17 or newer. Use a
disposable database role with schema creation rights.

The shared runtime contract suite runs against the `memory` backend (unit) and
the `postgres` backend (integration). The relay integration tests run when the
optional broker variables are set:

```sh
export REDIS_URL='redis://127.0.0.1:6379/0'
export KAFKA_BROKERS='127.0.0.1:9092'
go test -tags=integration ./internal/backend/relay/...
```

Relay codec, relayer, and notifier behavior is covered by zero-skip unit tests
regardless of broker availability.

Run the local delivery demo (builds images; requires Docker):

```sh
scripts/demo.sh memory
scripts/demo.sh evaluation retry
```

Build metadata can be overridden:

```sh
make build VERSION=v0.1.0 COMMIT="$(git rev-parse HEAD)" BUILD_DATE=2026-01-01T00:00:00Z
```

The reviewed protocol is under [`../spec/v1/`](../spec/v1/). The durability
model is under [`durability.md`](durability.md). The ingest handler, delivery
worker, and native backend profiles are implemented; keep new work within the
delivery data-plane scope and the reviewed contract, and do not add
control-plane or hosted-platform behavior that belongs in another repository
(see [`../README.md`](../README.md) for the repository topology).
Keep dependencies minimal and justify any new one in the pull request.

Build and validate release artifacts locally with the repo-native tool
(`make release-snapshot`); see [`releasing.md`](releasing.md).

The standard-library Go validator is always run. CI installs
`requirements-spec.txt` and requires the independent Draft 2020-12 validator;
local checks run it when those packages are available. They are test-only, not
runtime dependencies.
