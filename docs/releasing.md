# Releasing

`hook-service` builds release artifacts with a **repo-native, deterministic**
release tool rather than a third-party release toolchain. This keeps the
standard-library-first, minimal-dependency posture of the repository: the tool
drives the Go toolchain and the helpers in
[`internal/release`](../internal/release) using only the standard library, so a
snapshot or tagged build is reproducible and verifiable locally with no external
publish and no added module dependency.

> This tool is the GoReleaser-equivalent for this repository. It intentionally
> produces the same categories of artifacts — cross-platform archives, a
> checksum manifest, and SPDX SBOMs — deterministically from the Go toolchain.

## What it produces

For every supported target (`linux/amd64`, `linux/arm64`, `darwin/amd64`,
`darwin/arm64`) the tool builds both binaries and emits, into `dist/`:

- `hook-service_<version>_<os>_<arch>.tar.gz` — an archive containing
  `hookshipd`, `hookshipctl`, `LICENSE`, `NOTICE`, `THIRD_PARTY_NOTICES.md`, and
  `README.md`.
- `hook-service_<version>_<os>_<arch>.spdx.json` — an SPDX 2.3 SBOM describing
  the app, both binaries, and every Go module actually embedded in the built
  binary (with `pkg:golang` package URLs).
- `hook-service_<version>_checksums.txt` — a `sha256sum -c`-compatible manifest
  covering every archive and SBOM.
- `hook-service_<version>_manifest.json` — build metadata (version, commit,
  build date, source URL) and a per-artifact checksum list.

## Building locally

```sh
make release-snapshot          # untagged, dirty-tree-safe snapshot into dist/
make release-clean             # remove dist/

# Equivalent direct invocations:
go run ./scripts/release snapshot
go run ./scripts/release build -version v0.1.0
```

Neither mode publishes anything or reads any credential.

## Determinism

Builds are reproducible: identical inputs yield byte-identical archives and
checksums. This is achieved with `-trimpath`, `-buildvcs=false`,
`CGO_ENABLED=0`, `-ldflags "-s -w -buildid=…"`, fixed archive entry ordering and
zeroed ownership, a zeroed gzip header, and a build timestamp taken from
`SOURCE_DATE_EPOCH` (or the HEAD commit time) rather than wall-clock time.

Verify a rebuild is reproducible:

```sh
make release-snapshot && cp dist/*_checksums.txt /tmp/a.txt
make release-snapshot && diff /tmp/a.txt dist/*_checksums.txt   # no output == reproducible
```

Verify the published checksums:

```sh
cd dist && sha256sum -c *_checksums.txt   # or: shasum -a 256 -c
```

## Versioning

- `snapshot` derives the version from `git describe --tags --always --dirty`
  with a `-snapshot` suffix. It never requires a tag and is safe on a dirty
  tree, so it is the verification build run in CI and locally.
- `build -version vX.Y.Z` produces a tagged release build. Publishing tagged
  artifacts and container images is owned by the tag- and approval-gated release
  workflow, which runs this same tool under a protected environment. No token or
  registry identity lives in this repository.

`hook-service` is pre-release (`v0.x`) and has **no** published release yet;
this document describes how a release is built once one is cut.
