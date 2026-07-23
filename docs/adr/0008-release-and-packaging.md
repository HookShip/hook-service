# 0008 — Deterministic release tooling and single-VM packaging

Status: accepted (pre-release).

## Context

The delivery data plane is implemented, so the repository needs a way to produce
and deploy it. Two forces shaped the decision. First, the repository is
standard-library-first and pins/verifies every dependency; adding a large
third-party release toolchain (and its transitive dependencies) as a build input
conflicts with that posture and with the supply-chain story. Second, the
organization
[release policy](https://github.com/HookShip/.github/blob/main/RELEASE_POLICY.md)
requires signed tags, approval-gated publishing, SHA-pinned workflows, and no
committed credentials, and the deployment surface belongs to this repository per
the [placement policy](https://github.com/HookShip/.github/blob/main/REPOSITORY_PLACEMENT.md).

## Decision

- **Repo-native release builder.** Release artifacts are produced by a
  standard-library-only tool ([`internal/release`](../../internal/release),
  [`scripts/release`](../../scripts/release)) that drives the Go toolchain to
  build reproducible cross-platform archives, a `sha256sum`-compatible checksum
  manifest, and SPDX 2.3 SBOMs. It is the GoReleaser-equivalent for this
  repository and is validated locally with `make release-snapshot`. No
  third-party release toolchain is added as a dependency.
- **Tag- and approval-gated publishing.** A release workflow builds the tagged
  artifacts, pushes a multi-arch GHCR image derived from the actual repository,
  signs it keylessly with cosign via OIDC, records a SLSA build-provenance
  attestation, and publishes a GitHub Release. Publishing runs only in a
  protected environment; every third-party action is pinned to a commit SHA and
  no token is committed. External registry or KMS-backed signing stays
  credential-gated in repository settings.
- **Single-VM packaging.** The service is packaged for one Linux VM with
  hardened systemd units and an install/uninstall flow, plus source-build and
  pull-based (immutable-digest) Compose paths. Operational scripts and runbooks
  cover backup, restore, upgrade, schema-compatible rollback, and key rotation.

## Consequences

- Releases are reproducible and verifiable from a clean checkout without
  external tooling; the SBOM/checksum artifacts are produced deterministically.
- Because migrations are forward-only, a binary/image rollback is
  schema-compatible only; rollback tooling probes this and defers to a restore
  when the database is ahead of the code (ADR
  [0002](0002-postgres-source-of-truth.md), [0003](0003-at-least-once-delivery.md)).
- Concrete registry identities, environment reviewers, branch/tag protection,
  and signing trust are repository/organization settings, not tracked files, so
  they remain external gates that must be configured before a first release.
