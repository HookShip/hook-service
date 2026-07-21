# 0006 — Ephemeral memory profile is evaluation-only

Status: accepted (pre-release).

## Context

A fast deploy-and-test path must not be mistakable for durable acceptance.

## Decision

The `memory` profile implements every runtime port with bounded, process-local
state. It requires `HOOKSHIP_EVALUATION_MODE`, refuses production selection,
binds loopback, and declares `durability=ephemeral`. Every ingest response
carries an explicit ephemeral/evaluation marker and a distinct body that never
uses the durable receipt's `state: "durable"`. State is intentionally lost on
restart, and tests prove it.

## Consequences

Operators cannot accidentally accept production traffic on the memory profile.
The wire shape matches production where possible while remaining unmistakably
ephemeral.
