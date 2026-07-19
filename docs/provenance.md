# Provenance

## Initial repository

The initial `hook-service` repository was independently authored for HookShip
as a clean outbound webhook delivery data-plane foundation. It does not
continue, import, or vendor another service implementation.

## WAAS boundary

WAAS is an external MIT-licensed behavioral reference. No WAAS source code is
imported, copied, translated, or adapted in this initial repository, and WAAS
is not a build or runtime dependency.

Behavioral comparison does not grant permission to silently transfer
implementation. Before any future WAAS code or derived material is accepted,
the change must record:

1. the public upstream source location and exact revision;
2. the files, ranges, or other material used;
3. the applicable license and required notices;
4. whether the material was copied, modified, translated, or generated from;
5. the reviewing decision and resulting entry in
   [`../THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md).

If that record is absent, the repository policy is that no WAAS code has been
incorporated.

## HookShip toolkit protocol references

The v1 specification interoperates with, but does not import or depend on, the
public HookShip toolkit snapshot at Git revision
`1e8dc0267a1d77ca0cf256793693af0eba57f939`.
This is the latest locally available `origin/main` snapshot and its
repository-level `pnpm check` passed during this specification revision.

The referenced source declarations are:

- `packages/adapter-sdk/src/envelope.ts`:
  `COMMAND_ENVELOPE_SCHEMA_VERSION = "2026-07-01"` and
  `kind = "adapter_command"`;
- `packages/adapter-sdk/src/metadata.ts`:
  `CANONICAL_METADATA_SCHEMA_VERSION = "2026-07-01"`,
  `METADATA_INGEST_SCHEMA_VERSION = "2026-07-01"`, and
  `kind = "metadata_ingest"`;
- `packages/canonical-model/src/model.ts`:
  `CANONICAL_MODEL_VERSION = "1.0.0"` and
  `CANONICAL_SCHEMA_VERSION = "2026-07-01"`.

These are source-path and version citations only. No toolkit or private
platform source is copied, vendored, translated, or introduced as a source,
build, or runtime dependency. Compatibility is maintained through versioned
wire contracts and independent fixtures.

## PostgreSQL durability implementation

The durability schema, migration runner, repository, and tests were
independently authored for this repository from the reviewed HookShip v1
protocol. No database or delivery implementation was copied from WAAS, the
HookShip toolkit, or a private platform repository.

The implementation uses `github.com/jackc/pgx/v5` v5.10.0 under the MIT
License. Its module-selected transitive dependencies and licenses are recorded
in [`../THIRD_PARTY_NOTICES.md`](../THIRD_PARTY_NOTICES.md). PostgreSQL is an
external integration-test/runtime service, not vendored material.
