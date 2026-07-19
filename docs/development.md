# Development

## Requirements

- Go 1.26.5 or newer
- GNU Make or a compatible `make`
- Docker only for container validation

## Commands

```sh
make fmt          # format Go source
make fmt-check    # fail if source is not formatted
make test         # unit tests
make test-race    # unit tests with the race detector
make vet          # static checks from go vet
make build        # build bin/hookshipd and bin/hookshipctl
make validate     # repository structure validation
make check        # all local validation
```

Build metadata can be overridden:

```sh
make build VERSION=v0.1.0 COMMIT="$(git rev-parse HEAD)" BUILD_DATE=2026-01-01T00:00:00Z
```

Do not add delivery behavior before the protocol and durability gate described
in [`../spec/README.md`](../spec/README.md) is approved. Keep dependencies
minimal and justify any new one in the pull request.
