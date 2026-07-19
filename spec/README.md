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

The v1 contract satisfies the protocol-definition gate. The repository now
contains the reviewed PostgreSQL durability model for:

1. concrete persistence, idempotency, and pending recovery;
2. encrypted payload storage metadata and deletion states;
3. dispatch/outbox leasing, fencing, and immutable attempts.

The public event-ingest handler, outbound delivery worker, cloud storage
adapter, capacity claims, and implementation security review remain separate
gates. No runtime endpoint may claim conformance merely because the protocol
or persistence library exists. Run `make spec-validate` to validate the
versioned artifacts and see [`../docs/durability.md`](../docs/durability.md).
