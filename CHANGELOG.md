# Changelog

All notable changes to this project are documented here.

The format follows [Keep a Changelog](https://keepachangelog.com/en/1.1.0/). The
project intends to use [Semantic Versioning](https://semver.org/) once versions
are published.

`hook-service` is pre-release (`v0.x`). **No release has been tagged or
published, and no container image has been pushed.** The sections below describe
the current unreleased state, not a shipped release.

## [Unreleased]

Release status: unreleased. This is a pre-release data plane with no availability,
throughput, or service-level claims, and it has not been independently audited.

### Added

- Portable outbound webhook delivery data plane: protocol-v1 authenticated
  producer ingest, idempotent durable acceptance across the durability barrier,
  Standard Webhooks-compatible outbound signing, an SSRF-safe delivery worker
  with retry classification, circuit breaking, and a dead-letter queue, and
  payload-free `GET` status returning RFC 9457 problem responses.
- Selectable native backend profiles (`memory`, `postgres`, `postgres-redis`,
  `postgres-kafka`) with a versioned, machine-readable capability schema, and an
  S3-compatible object-store payload backend that stores AES-256-GCM ciphertext
  only.
- Static and AWS-compatible KMS payload key providers over a dependency-free
  AWS SigV4 signer; per-assignment/per-endpoint limits, retention workers, and
  payload-free operator metrics on `/statusz`.
- Forward-only PostgreSQL 17+ migrations with immutable checksums and a narrow
  pgx v5 repository; `hookshipd` (`api`/`worker`/`all`) and `hookshipctl`
  (`version`, `diagnostics`, `doctor`, `migrate`, `backend …`, `redrive …`).
- `hookshipctl redrive list|run`: an operator-facing dead-letter recovery
  surface that drives the same runtime backend ports as the worker
  (`ListDeadJobs`, `RedriveJob`) instead of mutating rows directly, and prints
  only payload-free job metadata.
- Repo-native, deterministic release tooling (`internal/release`,
  `scripts/release`, `make release-snapshot`) that builds reproducible
  cross-platform archives, a `sha256sum`-compatible checksum manifest, and SPDX
  2.3 SBOMs without a third-party release toolchain.
- Local integration image artifact (`make integration-image`,
  `scripts/build-integration-image.sh`): a credential-free `docker save`
  OCI/docker archive under `dist/integration/` with a portable `SHA256SUMS` and
  a `manifest.json` recording the image id and archive checksum, so an adjacent
  consumer can load and verify the image without any registry pull. The registry
  release path stays digest-pinned.
- Tag- and approval-gated release workflow that builds tagged artifacts, pushes
  a multi-arch (linux/amd64+arm64) GHCR image, signs it keylessly with cosign
  via OIDC, records a SLSA build-provenance attestation, and publishes a GitHub
  Release — all credential-gated behind a protected environment with no
  committed token. The Dockerfile cross-compiles via `BUILDPLATFORM`/`TARGETOS`/
  `TARGETARCH`.
- Single-VM packaging: hardened systemd units, `install.sh`/`uninstall.sh`, and
  source-build and pull-based (immutable-digest) Compose paths with a
  parameterized image reference.
- Operational scripts and runbooks: backup, restore, backup-age evidence,
  restore drill, upgrade, schema-compatible rollback, payload-key/KMS rotation,
  PostgreSQL and MinIO recovery, and log rotation.
- Documentation: deployment, runbooks, troubleshooting, compatibility matrix,
  releasing, cross-repository navigation and source-of-truth links, and an
  offline docs-integrity check (`scripts/validate-docs.py`) wired into CI.
- Adoption of the organization's reusable Go-validation and secret-scan
  workflows alongside the existing PostgreSQL/Redis/MinIO and container
  integration jobs.
- OpenMetrics `/metrics` endpoint on the operational listener, exposing the
  existing payload-free counters and gauges (namespaced `hookship_`) in the
  standard text exposition format for a Prometheus/OpenMetrics scraper.
- Pinned `golangci-lint` (v2) configuration and gate (`.golangci.yml`,
  `make lint`, wired into `make check`) layering error-handling and correctness
  linters on top of `go vet`; formatting stays owned by `gofmt`.

### Changed

- Operator status (`/statusz`) and the new metrics endpoint (`/metrics`) are now
  served on a separate, loopback-only operational listener
  (`HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS`, default `127.0.0.1:9090`) instead of
  the ingest listener, so operational information is never exposed on an
  externally bound producer surface. A non-loopback operational bind fails
  closed. `/healthz` and `/readyz` remain on both listeners, so load balancers
  and `hookshipctl diagnostics` are unaffected.
- Bumped `golang.org/x/text` to v0.39.0 to clear the reachable GO-2026-5970
  advisory (infinite loop on invalid input), and added a pinned `govulncheck`
  gate (`make vulncheck`, run in `make ci`) so future reachable vulnerabilities
  fail CI.
- Documentation corrected to describe the implemented delivery runtime and the
  deployment surface, removing pre-implementation scope language that predated
  the delivery runtime.

### Security

- Replaced the deterministic, `whsec_`-shaped Standard Webhooks test secrets in
  the evaluation default (`internal/assignmentconfig/defaults.go`), the
  assignment-config and worker tests, the local-sink and Compose examples, and
  the signing conformance vectors with plain, self-describing, non-provider HMAC
  keys, so no committed fixture or default is shaped like a provider signing
  secret. The `whsec_` base64 decode path is now covered by a unit test that
  builds the secret at runtime. Signing semantics are unchanged (recomputed
  golden signatures verify identically).
- Tightened `.gitleaks.toml`: removed the broad whole-file path allowlists (which
  could have hidden a real secret added to those files) and kept only two
  exact-value allowlist entries for the well-known fabricated test strings that
  remain in earlier git history, so a full-history scan stays green without a
  history rewrite while any new secret is still caught.

### Fixed

- Running `api` and `worker` as separate processes on one host now fails fast
  with an actionable error when they share an operational address: the
  operational listener's address-in-use error names the required distinct
  `HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS`. The split same-host topology requirement
  (distinct operational and ingest listener addresses) is documented in the
  configuration and operations guides.

[Unreleased]: https://github.com/hookship/hook-service/commits/main
