# Governance

This project is maintained under a lightweight, maintainer-led model appropriate
for an early-stage open-source project. This document describes how it works
today and how that is expected to evolve. It defers to the organization-wide
policies in [`HookShip/.github`](https://github.com/HookShip/.github) and adds
the specifics for this repository.

## Decision-making

- Maintainers review and merge changes through pull requests. Non-trivial
  changes should be discussed in an issue before significant implementation
  work, so direction is agreed before effort is spent.
- Maintainers favor consensus. When maintainers disagree and discussion does not
  converge, the maintainer most active in the affected area makes the final call
  and explains the reasoning in the relevant issue or pull request.
- Significant, hard-to-reverse decisions are recorded as
  [architecture decision records](docs/adr/). A later record that supersedes an
  earlier one links back to it, and the earlier record is marked superseded
  rather than deleted, so the reasoning lineage stays auditable.

## Scope boundary enforced in this repository

This repository owns the **outbound delivery data plane, protocol, and
deployment surface** and nothing else. Contributions that add a hosted
multi-tenant control plane, billing/metering, tenancy, or general-purpose
toolkit primitives are out of scope; they belong in `platform` or `toolkit`
respectively, per the organization
[repository placement policy](https://github.com/HookShip/.github/blob/main/REPOSITORY_PLACEMENT.md)
and [source-of-truth rules](https://github.com/HookShip/.github/blob/main/SOURCE_OF_TRUTH.md).
Where this protocol builds on contract or signing behavior, `toolkit` is the
source of truth and this repository references it rather than restating it.

## Required checks and releases

- Changes reach `main` only through a pull request that passes the required
  checks (build, static analysis, tests, and the verified secret scan).
- Releases follow the organization
  [release policy](https://github.com/HookShip/.github/blob/main/RELEASE_POLICY.md):
  releases are cut only from a protected-branch commit that passed all required
  checks, under an annotated and signed tag, and publishing runs in a protected
  environment that requires human approval. Branch protection, tag protection,
  required reviewers, and any registry/signing identities are configured in
  repository settings and are never committed to version control.

## Becoming a maintainer

There is no fixed contributor-to-maintainer ladder yet. In practice, sustained,
high-quality contributions (code, review, documentation, or triage) are how
someone becomes a maintainer; an existing maintainer proposes it and other
maintainers have the opportunity to object before it is final.

## Licensing and contributions

- Contributions are accepted under the project's
  [Apache-2.0 license](LICENSE); by submitting a pull request you agree your
  contribution is licensed under the same terms.
- Record third-party provenance and licensing when external material is used
  ([`docs/provenance.md`](docs/provenance.md)).
- Contributors are encouraged to add a `Signed-off-by` line
  (`git commit --signoff`) as a Developer Certificate of Origin-style
  attestation. This is currently a convention, not a CI-enforced requirement.

## Changes to this document

This document reflects current practice for a small, early-stage project and is
expected to change as the contributor and maintainer base grows. Proposed
changes go through the normal pull request process.
