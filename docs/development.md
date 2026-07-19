# Development

## Requirements

- Go 1.26.5 or newer
- GNU Make or a compatible `make`
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
make check        # all local validation
make gitleaks     # require and run the local gitleaks binary
```

Database integration tests are intentionally separate:

```sh
export TEST_DATABASE_URL='postgres://hookship@127.0.0.1:5432/hookship_test?sslmode=disable'
make test-integration
```

The target fails when the variable is absent. Tests create and drop isolated
schemas, apply migrations from zero, and require PostgreSQL 17 or newer. Use a
disposable database role with schema creation rights.

Build metadata can be overridden:

```sh
make build VERSION=v0.1.0 COMMIT="$(git rev-parse HEAD)" BUILD_DATE=2026-01-01T00:00:00Z
```

The reviewed protocol is under [`../spec/v1/`](../spec/v1/). The durability
model is under [`durability.md`](durability.md). Do not add the public ingest
handler, delivery workers, or runtime control-plane behavior before their
remaining gates in [`../spec/README.md`](../spec/README.md) are satisfied.
Keep dependencies minimal and justify any new one in the pull request.

The standard-library Go validator is always run. CI installs
`requirements-spec.txt` and requires the independent Draft 2020-12 validator;
local checks run it when those packages are available. They are test-only, not
runtime dependencies.
