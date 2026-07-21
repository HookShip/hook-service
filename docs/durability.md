# Durability model v1

## Status

Accepted for the persistence-library phase. This decision does not add the
public event-ingest handler, an outbound delivery worker, cloud object-store
code, or a production readiness claim.

## Decision

Each regional cell owns one PostgreSQL 17+ database. Assignment identity is
the tenant isolation boundary in this database; no control-plane tenant,
region, fleet, or global routing tables are on the ingest path. Stable IDs are
created by the caller and every relationship uses an assignment-scoped
composite foreign key.

The schema stores:

- a `pending`/`committed`/`tombstone` idempotency ledger keyed uniquely by
  assignment and idempotency key;
- immutable event ingest rows containing only stable headers and hashes;
- encrypted payload metadata and either bounded ciphertext in PostgreSQL or
  deterministic object-store staging/final identities;
- immutable endpoint/config generation snapshots with exact content bytes and
  SHA-256, sufficient for delivery without control-plane access;
- delivery and immutable bounded attempt lineage;
- dispatch jobs and payload-free metadata outbox rows with leases and fencing.

There is no broker dual write. PostgreSQL is the durable queue. A replay-
protection nonce ledger (`(credential_id, nonce)`) was added as an additive
forward migration (`000002_nonce_ledger`); it does not alter any existing
acceptance semantics. Redis/Kafka profiles publish only advisory dispatch
signals derived from committed jobs — see
[`adr/0007-dispatch-relays-no-dual-write.md`](adr/0007-dispatch-relays-no-dual-write.md).

## Acceptance transaction

`AcceptPostgres` locks a matching pending ledger and atomically inserts the
ingest, committed ciphertext record, delivery, and ready dispatch job before
transitioning the ledger to committed. A deferred database constraint trigger
independently rejects a committed ledger unless all barrier artifacts match
its assignment, request fingerprint, ingest, payload, and ready dispatch job.

Object storage uses a recoverable protocol:

1. reserve the idempotency ledger as pending;
2. call `GetObjectPayloadStage` by assignment and ledger before any provider
   I/O;
3. when a stage exists, resume its exact payload ID, deterministic staging and
   final keys, key reference/version, nonce, checksum, size, and expiry;
4. when no stage exists, generate ciphertext metadata and call
   `StageObjectPayload` to persist that exact identity before provider I/O;
5. claim `ClaimObjectStageWrite`; the database returns the current
   attempt-specific staging/final keys, owner/token fence, and database-clock
   lease expiry;
6. write encrypted bytes to that attempt key using provider create-only
   semantics and record the provider object version;
7. conditionally promote to that attempt-specific final candidate and call
   `FinalizeObjectPayload` with both provider versions and the enforced
   precondition mode;
8. atomically insert ingest/delivery/job, mark payload committed, and commit
   the ledger.

Restarted processes must load rather than regenerate the stage. A changed key,
key version, nonce, checksum, size, object identity, staged time, or expiry is
an idempotency conflict. An expired write lease orphans its attempt and a
reclaim creates a monotonically numbered staging identity; the stale identity
remains in `payload_stage_write_attempts`. Only the current unexpired lease may
finalize. Before step 8, retries remain pending and never return a duplicate
receipt.
Staging TTL is at least the 24-hour retry horizon. Expired staging/finalized
records become orphaned and enter deletion cleanup; once orphaned, retries
remain unavailable rather than falsely reporting a durable duplicate. The
repository deliberately does not implement provider I/O.

`AcceptFinalizedObject` locks the payload and compares `expires_at` with
PostgreSQL `clock_timestamp()`. Caller acceptance timestamps cannot extend an
expired object.

### Provider preconditions

An object provider adapter is conforming only if it can:

- create an attempt-specific staging object conditionally and expose an
  immutable version/generation identifier;
- create/promote the attempt-specific final candidate with create-only or
  exact-version-match semantics, or verify an existing candidate version with
  the stored ciphertext checksum;
- prevent an unconditional overwrite of either immutable attempt object;
- make delete and version lookup idempotent.

The adapter records `create_only`, `match_version`, or `verified_existing`
along with staging/final provider versions. A stale writer can at most recreate
its own retained attempt keys; it cannot make that candidate the payload's
selected final object after losing the database fence.

## Idempotency

The request fingerprint is a domain-separated SHA-256 over the exact body
hash plus event ID, event type, route, and content type. Timestamp, nonce,
signature, and rotating credential ID are excluded. Concurrent reservation
uses the unique `(assignment_id, idempotency_key)` constraint and row locking.

Committed receipts retain a retry expiry at least 24 hours after acceptance.
While classifying a retry under the row lock, the repository first changes an
expired committed receipt to a terminal tombstone. Expired classification
precedes body or stable-header conflict classification, so an expired key can
never be reported as a duplicate or reusable conflict. Tombstone expiry is at
least another 24 hours later. Payload deletion requires a tombstoned ledger;
ledger and immutable bounded metadata may be retained longer.

## Leases and attempts

Jobs and outbox rows are claimed with `FOR UPDATE SKIP LOCKED`. A claim records
owner, random token, expiry, and incremented attempt fence. Expired leases are
eligible for reclamation. Settlement compares assignment, row ID, owner,
token, attempt fence, state, and unexpired lease. Claim eligibility, lease
creation, stale reclaim, and settlement all use PostgreSQL
`clock_timestamp()`; worker clocks never decide lease ownership. Retry APIs
accept a delay and calculate the next `run_at` from the database clock.
Exhausted work becomes dead.

Attempts are inserted complete and immutable. Attempts and outbox rows expose
only allowlisted metadata columns; neither table has payload, ciphertext,
arbitrary header, or response-body storage.

## Retention

Partial indexes cover pending recovery, committed/tombstone expiry, payload
TTL/orphans, runnable leases, dead jobs, attempts, and published outbox rows.
Cleanup is bounded and uses `SKIP LOCKED`. PostgreSQL ciphertext is cleared
when deletion completes. A committed payload is not eligible while any job
that references it is ready, leased, or retryable. Object deletion completion
is recorded only after a future provider adapter confirms both object
identities are absent.

Payload deletion itself is leased and fenced. A claim records owner, token,
database-calculated expiry, and an incremented deletion attempt. PostgreSQL
ciphertext remains present until fenced completion. An interrupted object
deletion is reclaimed after lease expiry with the same deterministic staging
and final keys, making repeated provider deletes safe. A stale owner cannot
complete either backend after another worker reclaims the lease.

`ListObjectStageCleanup` returns every attempt key and provider version,
including orphaned and promoted attempts, after payload deletion completes.
Provider cleanup must delete/check every retained attempt staging/final key
before completion, then continue periodic rechecks.
Attempt rows are never removed by deletion completion, so a late stale
recreation remains discoverable rather than becoming an ABA-hidden object.

## Object state machine

```text
payload staging
  └─ attempt ready -> leased
       ├─ lease expiry -> attempt orphaned + new numbered attempt leased
       └─ conditional provider promotion + fenced DB finalize
            -> attempt promoted + payload finalized

payload finalized
  ├─ unexpired acceptance transaction -> committed
  └─ TTL expiry -> orphaned

payload committed/orphaned
  -> deletion leased
  -> stale deletion lease reclaimed with a higher attempt fence
  -> deleted (attempt cleanup catalog retained)
```

Retention cannot orphan a payload, and deletion cannot claim it, while a
database-current stage-write lease exists.

## Alternatives rejected

- Database plus Kafka/Redis: creates an avoidable dual-write recovery problem.
- Exactly-once delivery: leases are intentionally at-least-once.
- Mutable endpoint lookup at delivery time: makes control-plane availability
  part of delivery correctness.
- Plaintext payload columns or attempt response bodies: unnecessary exposure.
