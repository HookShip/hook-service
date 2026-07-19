# Compatibility and versioning

## Independent versions

The three channels negotiate independently:

- HookShip `event_ingest` HTTP protocol: major version `1`, selected by `/v1`;
- toolkit `adapter_command` schema: `2026-07-01`;
- toolkit canonical metadata and `metadata_ingest` schemas: `2026-07-01`;
- toolkit canonical model: model `1.0.0`, schema `2026-07-01`.

An implementation MUST NOT infer support for one channel from support for
another. The toolkit versions are citations to the current public source
snapshot recorded in [`../../docs/provenance.md`](../../docs/provenance.md);
there is no source dependency.

## Change policy

Within `/v1`, changes may:

- clarify text without changing observable behavior;
- add optional response fields only when old clients can ignore them;
- add new enum values only where the schema and client contract explicitly
  permit unknown values.

Within `/v1`, changes MUST NOT:

- reinterpret signed fields or canonicalization;
- weaken credential-derived identity or replay protection;
- alter idempotency identity or the durability acknowledgement barrier;
- return payload data from status;
- make an optional request field required;
- add a problem `code` to the closed problem schema;
- repurpose an existing problem code or status.

Any such change requires a new HTTP major version and new fixtures. JSON Schema
documents are closed by default; their `schemaVersion` fields change when
incompatible configuration or fleet shapes change.

## Compatibility acknowledgement

Future fleet members report explicit supported version sets and acknowledge a
specific configuration bundle schema, generation, and checksum. Absence of an
acknowledgement is incompatibility, not implicit consent. A controller MUST
retain the last-known-good bundle until a newer compatible bundle is verified
and applied.

The executable checksum manifest, Draft 2020-12 identifier registry, schema
validation, reference checks, signing vectors, and configuration vectors are
run by `make spec-validate`.
