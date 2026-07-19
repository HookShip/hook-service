# Private first-party adapter mapping

The private HookShip platform integrates through a first-party adapter without
sharing source code or state with this repository.

## Control plane: `adapter_command`

The adapter continues to consume the toolkit's signed `adapter_command`
envelopes for capability-driven endpoint, subscription, test, replay, and
related control operations. It translates supported operations into private
control intents and future configuration-bundle generations for
`hook-service`.

An `adapter_command` is never sent to `POST /v1/event-ingests`, and its command
JSON is never persisted as a producer event payload.

Current public toolkit citation:

- revision `1e8dc0267a1d77ca0cf256793693af0eba57f939`;
- `packages/adapter-sdk/src/envelope.ts`;
- kind `adapter_command`;
- schema version `2026-07-01`.

## Producer data plane: `event_ingest`

Producers call the raw-body HTTP API directly with credentials scoped to an
assignment. The private adapter may provision or rotate those assignments, but
it does not wrap event bodies in toolkit command or metadata envelopes.

Tenant, environment, and producer identity are resolved by `hook-service` from
the verified assignment. The platform cannot override them with arbitrary
request fields.

## Observation plane: `metadata_ingest`

Future delivery execution emits only canonical allowlisted delivery-attempt
metadata. The first-party adapter maps those records into the toolkit's signed
`metadata_ingest` envelope and forwards them to the private platform.

It MUST NOT include raw event bodies, payload previews, arbitrary producer
headers, delivery secrets, or payload download references.

Current public toolkit citations at the same revision:

- `packages/adapter-sdk/src/metadata.ts`;
- canonical metadata schema `2026-07-01`;
- `metadata_ingest` schema `2026-07-01`;
- `packages/canonical-model/src/model.ts`;
- canonical model `1.0.0`, schema `2026-07-01`.

## Compatibility

The adapter pins an explicit supported range for each independent channel and
is tested against copied wire fixtures, not sibling imports or private source.
Unsupported versions fail closed while the last-known-good compatible
configuration remains active.
