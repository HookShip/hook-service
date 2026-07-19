# HookShip delivery specifications

## Status

No public webhook delivery protocol or durability contract is defined in this
initial repository. This directory is a deliberate gate, not an undocumented
API placeholder.

The next phase must specify and review the protocol and durability boundaries
before implementation. That work should define only behavior supported by
explicit use cases and testable failure semantics. It must not be inferred
from the operational health endpoints or from external reference
implementations.

Candidate concerns may be admitted to the specification process only when
requirements exist for them. This repository does not currently choose wire
formats, delivery submission APIs, persistence guarantees, retry behavior,
ordering behavior, idempotency behavior, signing behavior, or tenant
boundaries.

## Gate for implementation

Delivery implementation may begin only after:

1. scope and non-goals are reviewed;
2. protocol and durability decisions are documented;
3. compatibility and failure behavior have executable acceptance criteria;
4. security and sensitive-data handling are reviewed;
5. provenance remains explicit for any external reference material.
