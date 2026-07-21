# Backend capability contract `backend_capabilities/v1`

This directory is the **authoritative, versioned cross-repo contract** for
backend capability documents, owned by `hook-service`. It is intentionally
separate from the HTTP protocol contract in [`../v1/`](../v1/) because it has its
own version, `backend_capabilities/v1`.

## Artifacts

- [`schema.json`](schema.json): JSON
  Schema (Draft 2020-12). Its `$id` is the local URN
  `urn:hookship:backend-capabilities:v1`; it is deliberately not a resolvable
  web URL.
- [`fixtures/`](fixtures/): one canonical document per built-in profile
  (`memory`, `postgres`, `postgres-redis`, `postgres-kafka`, and the reserved
  `kafka-native-ledger`). Each fixture is the byte-exact output of
  `hookshipctl backend describe <profile>`.
- [`checksums.txt`](checksums.txt): deterministic SHA-256 manifest.

## Canonical shape

Every document is:

```json
{
  "$schema": "urn:hookship:backend-capabilities:v1",
  "schemaVersion": "backend_capabilities/v1",
  "kind": "backend_capability",
  "profile": "<profile>",
  "properties": { "displayName": "...", "summary": "...", "evaluationOnly": false, "status": "...", "reasonCode": "...", "reason": "...", "...": "..." },
  "capabilities": { "acknowledgementBarrier": { "supported": true, "status": "...", "reasonCode": "...", "reason": "..." }, "...": "..." },
  "bounds": { "maxPayloadBytes": 0, "maxAssignments": 0, "maxInFlightJobs": 0, "testedThroughputPerSec": 0 },
  "requirements": { "backupRequirement": "...", "requiredExternalServices": [], "deploymentModes": [] }
}
```

`hookshipd capabilities -output=json` emits a `{ "schemaVersion", "profiles": [ ... ] }`
envelope whose `profiles` elements are exactly these documents.

## Cross-repo consumption

`platform` (and any other consumer) must copy the schema and fixtures
byte-for-byte from this directory, pin the exact supported version
(`backend_capabilities/v1`), and record the source revision in its provenance.
No sibling source dependency is permitted. When no release exists, consumers
pin the working-tree/unreleased revision honestly.

## Regenerating fixtures

Fixtures are generated from the Go source of truth:

```sh
go test ./internal/capabilities -run TestCanonicalFixturesMatchOutput -update
python3 scripts/validate-backend-capabilities.py
```
