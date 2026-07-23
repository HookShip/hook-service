# HookShip protocol specifications

## Status

The first reviewed protocol specification is versioned under [`v1/`](v1/).
It defines contracts and acceptance criteria, not a running API.

The specification explicitly separates:

1. toolkit `adapter_command` control-plane commands;
2. producer `event_ingest` v1 requests containing exact raw event bodies;
3. toolkit `metadata_ingest` payload-free delivery observations.

Producer events are never represented as metadata observations. Adapter
commands never carry producer event bodies.

## Implementation status

The v1 contract satisfies the protocol-definition gate, and the repository
implements it: the reviewed PostgreSQL durability model, the authenticated
public event-ingest handler, the outbound delivery worker, and the
S3-compatible object-store payload backend all run against this contract.

The contract remains the source of truth: a runtime endpoint conforms only when
it satisfies the versioned artifacts, not merely because the protocol or a
persistence library exists. Capacity/throughput claims and an independent
implementation security review are explicitly out of scope and are not asserted.
Run `make spec-validate` to validate the versioned artifacts and see
[`../docs/durability.md`](../docs/durability.md).
