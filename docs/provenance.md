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
