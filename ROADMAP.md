# Roadmap

`hook-service` is a pre-release (`v0.x`) project. This roadmap describes
direction, not delivery dates or service commitments. It makes no availability,
throughput, or service-level claims.

## Current: single-cell delivery data plane

Implemented in this repository:

- Protocol-v1 authenticated producer ingest, idempotent durable acceptance,
  Standard Webhooks-compatible signing, an SSRF-safe delivery worker with
  retries/DLQ, and payload-free status.
- Selectable native backend profiles (`memory`, `postgres`, `postgres-redis`,
  `postgres-kafka`) with a versioned capability schema, an S3-compatible
  object-store payload backend, and static/AWS-KMS payload key providers.
- Per-assignment/per-endpoint limits, retention workers, and payload-free
  operator metrics.
- Forward-only PostgreSQL 17+ migrations and a narrow pgx v5 repository.
- Repo-native deterministic release tooling, a tag- and approval-gated
  release/GHCR workflow with keyless provenance, single-VM systemd and Compose
  packaging, and backup/restore/upgrade/rollback/rotation runbooks.

No release has been tagged or published, and no image has been pushed.

## Next: first tagged pre-release

- Exercise the tag- and approval-gated release workflow end to end once the
  protected `release` environment, branch protection, and required checks are
  configured in repository settings (see the org
  [release policy](https://github.com/HookShip/.github/blob/main/RELEASE_POLICY.md)).
- Keep the reproducible artifacts, SBOMs, checksums, and keyless image
  provenance verifiable from a clean checkout.
- Publish only from a protected-branch commit that passed all required checks,
  under an annotated, signed tag.

Concrete registry identities, environment reviewers, and signing trust are
repository/organization settings and are never committed here.

## Later: hardening and evidence

- Broaden recovery evidence (recurring restore drills) and operational metrics.
- Add backends, standards, or versions only with fixtures, conformance tests,
  and explicit capability-schema updates.
- Strengthen supply-chain evidence (attestations, verification tooling) as it
  becomes practical without external credentials in source control.

## Out of scope for this repository

- A hosted multi-tenant control plane, billing/metering, or tenancy — these
  belong in the private `platform` repository.
- General-purpose toolkit primitives, contracts, or signing libraries — these
  belong in `toolkit`, which this repository consumes as the source of truth.
- Exactly-once delivery; delivery is at-least-once and downstream idempotency
  remains necessary.
- Any availability, throughput, certification, compliance, or production-
  readiness claim, and any invented domain, registry, or contact.

See the organization
[repository placement policy](https://github.com/HookShip/.github/blob/main/REPOSITORY_PLACEMENT.md)
for the full topology.
