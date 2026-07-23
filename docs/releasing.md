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

## Publishing workflow

[`.github/workflows/release.yml`](../.github/workflows/release.yml) is tag- and
approval-gated. On a `vX.Y.Z` tag it runs the gates, then, only after the
protected `release` environment is approved, it publishes. Two properties are
enforced fail-closed before anything is published:

- **Tag signature verification.** [`scripts/verify-release-tag.sh`](../scripts/verify-release-tag.sh)
  refuses to proceed unless the tag is annotated **and** produces a good GnuPG
  signature from the trusted key. The org
  [release policy](https://github.com/HookShip/.github/blob/main/RELEASE_POLICY.md)
  requires signed, verifiable tags. Verified locally by
  [`scripts/verify-release-tag.test.sh`](../scripts/verify-release-tag.test.sh)
  (fail-closed cases always; signature cases where `gpg` is present).
- **One normalized image reference.** The GHCR reference is lowercased once via
  `go run ./scripts/release image-ref` (backed by
  [`release.NormalizeImageRef`](../internal/release/release.go), unit-tested) so
  a mixed-case owner such as `HookShip/hook-service` yields
  `ghcr.io/hookship/hook-service`. That single subject is reused for the pushed
  tags, the cosign signature, and the provenance attestation, so the signed
  digest and the pushed digest never diverge.

### External settings (never committed)

These must be configured in repository/organization settings before a release:

| Setting | Purpose |
| --- | --- |
| `release` environment + required reviewers | Gate publishing behind human approval |
| `RELEASE_TAG_GPG_PUBLIC_KEY` (environment secret) | The trusted public key used to verify the release tag signature. Provide the ASCII-armored public key; the private signing key stays with the maintainer and is never stored in the repo or CI. |
| Branch/tag protection, required checks | Per the org release policy |

Any external registry or KMS-backed signing beyond GHCR keyless is likewise a
protected-environment secret and is never committed here.

## Local integration image artifact

Separately from the registry release, `make integration-image` (a thin wrapper
around [`scripts/build-integration-image.sh`](../scripts/build-integration-image.sh))
produces a **local, credential-free** OCI/docker archive that an adjacent
consumer — for example the platform integration harness — can load and verify
without pulling from any registry and without spending on any hosted service.

It writes, into `dist/integration/`:

- `hook-service-integration-<commit>.tar` — a `docker save` archive of the image
  built from this tree's [`Dockerfile`](../Dockerfile);
- `SHA256SUMS` — a portable checksum of the archive bytes;
- `manifest.json` — the image reference, the image id (config digest), the
  archive filename and `archiveSha256`, and source provenance (commit, build
  date, source URL).

A consumer verifies the archive bytes (`archiveSha256`) before `docker load`,
then confirms the loaded image identity (`imageId`) after load. This is an
integration convenience only. It does **not** replace or weaken the release
path: production consumers still pull the registry image by immutable
`…@sha256:` digest via [`deploy/compose/compose.pull.yaml`](../deploy/compose/compose.pull.yaml).

`hook-service` is pre-release (`v0.x`) and has **no** published release yet;
this document describes how a release is built once one is cut.
