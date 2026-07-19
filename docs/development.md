# Development

## Requirements

- Go 1.26.5 or newer
- GNU Make or a compatible `make`
- Docker only for container validation
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
make check        # all local validation
```

Build metadata can be overridden:

```sh
make build VERSION=v0.1.0 COMMIT="$(git rev-parse HEAD)" BUILD_DATE=2026-01-01T00:00:00Z
```

The reviewed protocol is under [`../spec/v1/`](../spec/v1/). Do not add
delivery workers, databases, or runtime control-plane behavior before their
remaining gates in [`../spec/README.md`](../spec/README.md) are satisfied.
Keep dependencies minimal and justify any new one in the pull request.

The standard-library Go validator is always run. CI installs
`requirements-spec.txt` and requires the independent Draft 2020-12 validator;
local checks run it when those packages are available. They are test-only, not
runtime dependencies.
