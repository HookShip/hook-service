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

## Gate for implementation

The v1 contract satisfies the protocol-definition gate. Runtime work remains
blocked until an implementation review defines and tests:

1. the concrete persistence model and crash recovery;
2. payload storage and deletion behavior;
3. dispatch leasing, retry scheduling, and delivery attempt semantics;
4. operational limits and capacity behavior;
5. security review of the implementation.

No runtime endpoint may claim conformance merely because these files exist.
Run `make spec-validate` to validate the versioned artifacts.
