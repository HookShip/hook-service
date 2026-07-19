# Event ingest protocol v1

## 1. Transport security

Every non-loopback request MUST use HTTPS. Servers MUST support TLS 1.2 or
newer and SHOULD prefer TLS 1.3. Clients and servers MUST NOT downgrade to
plaintext, follow or emit an HTTPS-to-HTTP redirect, or send credentials after
a plaintext redirect. A plaintext non-loopback listener MUST reject the
connection without processing authentication headers or request bodies.

Plain HTTP is permitted only when all of the following are true:

1. the listener and client destination are loopback addresses;
2. an operator explicitly enabled non-production evaluation mode;
3. the credentials are marked non-production and cannot authenticate to any
   non-loopback or production assignment;
4. no proxy, tunnel, forwarded host, or redirect leaves loopback.

HMAC authentication does not replace TLS confidentiality or server
authentication.

## 2. Identity and authorization

`POST /v1/event-ingests` accepts a producer event. The verified scoped
credential resolves to an immutable assignment containing the tenant,
environment, producer, allowed routes, and limits.

Clients MUST NOT submit tenant, environment, or producer identifiers in the
request body or headers. The service MUST derive those identities from the
verified credential and assignment. Credential rotation MUST preserve the
assignment identity used for idempotency.

The request headers are:

- `Authorization: Hookship-HMAC Credential=<id>,Signature=<base64url>`;
- `X-Hookship-Timestamp`: exactly `YYYY-MM-DDTHH:MM:SSZ`;
- `X-Hookship-Nonce`: a fresh base64url value;
- `Idempotency-Key`: stable for one producer operation;
- `X-Hookship-Event-ID`: producer-stable event identifier;
- `X-Hookship-Event-Type`: stable event type;
- `X-Hookship-Route`: assignment-authorized logical route;
- `X-Hookship-Body-SHA256`: lowercase hexadecimal SHA-256 of the exact entity
  body;
- `Content-Type`: the media type of those exact bytes.

Multiple instances of any signed header, query parameters, non-identity
`Content-Encoding`, and request-target normalization are rejected. Signing is
defined in [`signing.md`](signing.md). Clients MUST reject every redirect for
signed requests because the path and transport authority are security
boundaries.

## 3. Raw-body preservation

The entity body after HTTP transfer-coding removal, but before any content
decoding, parsing, Unicode conversion, or reserialization, is the event
payload. The service MUST:

1. enforce the assignment's bounded byte limit while streaming;
2. hash the exact bytes and compare them with `X-Hookship-Body-SHA256`;
3. commit exactly those bytes and the signed `Content-Type`;
4. never require the payload to be JSON.

The service MUST NOT decompress, normalize, parse, or log the raw body during
ingest. `Content-Encoding` other than absent or `identity` is rejected with
415 so that stored bytes are unambiguous.

## 4. Replay and idempotency

Replay protection and business idempotency are separate:

- `(credential_id, nonce)` is a replay identity. A timestamp MUST be within
  300 seconds of server time. An accepted nonce is retained for at least 600
  seconds after first observation and at least until 600 seconds after its
  signed timestamp, whichever is later.
- `(assignment_id, Idempotency-Key)` is the idempotency identity. It does not
  change when an assignment credential rotates.

The idempotency ledger has these durable states:

- `pending`: the identity and request fingerprint are reserved, but one or
  more acknowledgement-barrier artifacts are incomplete;
- `committed`: the ingest record, ledger entry, exact payload commit, and
  dispatch job have all crossed the durability barrier;
- `tombstone`: the committed receipt is no longer eligible for duplicate
  replay, but the identity and fingerprint remain terminal and cannot be
  reused.

A retry MUST reuse the same `Idempotency-Key`, raw body, body hash, event ID,
event type, route, and content type, but MUST use a fresh timestamp, nonce, and
signature.

If the idempotency identity already exists:

- a `committed` entry with the same body hash and stable event headers returns
  the original ingest as a duplicate with HTTP 200;
- a `pending` entry with the same request MUST be resumed or repaired; if the
  complete barrier cannot be crossed during the request, the service returns
  retryable HTTP 503 and MUST NOT return 200;
- a `tombstone` returns HTTP 409 `idempotency_expired`;
- a different body hash returns HTTP 409 `idempotency_conflict`;
- the same body hash with different event ID, event type, route, or content
  type returns HTTP 409 `idempotency_metadata_conflict`.

Duplicate responses contain the original ingest ID and acceptance timestamp.
They do not create a second payload or dispatch job.

The v1 retry horizon is 24 hours. A committed ledger entry and its original
receipt MUST remain available until `idempotencyExpiresAt`, which MUST be at
least 24 hours after `acceptedAt`. At expiry it MUST become a tombstone retained
for at least another 24 hours. During that tombstone period the key cannot be
reused. After tombstone expiry an implementation MAY forget the key; producers
SHOULD therefore generate globally unique keys and MUST NOT depend on
deduplication beyond the advertised expiry.

Nonce retention is the short replay window above and is independent of the
idempotency retry horizon.

## 5. Durability acknowledgement barrier

An initial request returns HTTP 202 only after all four artifacts are durable:

1. the immutable ingest record;
2. the idempotency ledger entry;
3. the exact payload commit;
4. the dispatch job referencing that committed payload and ingest.

The implementation MAY use one atomic transaction or a recoverable commit
protocol, but it MUST NOT return success while any artifact is only in memory
or can be silently lost. A failure before the complete barrier returns 503 and
is retryable with the same idempotency identity and payload. Recovery MUST
inspect `pending` entries and either complete the original artifacts or keep
returning retryable 503; it cannot manufacture a committed result.

This is an acceptance guarantee, not an exactly-once delivery guarantee.
Future dispatch workers may execute jobs more than once after failures.
Downstream delivery idempotency remains necessary.

## 6. Success and status

Initial acceptance returns 202. An idempotent duplicate returns 200. Both
return:

- `Location` with the payload-free status path;
- `Hookship-Request-Id`;
- `Hookship-Ingest-Id`;
- `Hookship-Duplicate: true|false`;
- `Hookship-Idempotency-Expires-At`;
- an `EventIngestReceipt` body.

`GET /v1/event-ingests/{ingest_id}` uses the same scoped HMAC authentication.
Only the assignment that owns an ingest may observe it. The response may
contain stable event identifiers, content type, body hash, acceptance time,
idempotency expiry, and dispatch-job state. It MUST NOT contain, reconstruct,
preview, or provide a download reference for the raw payload or arbitrary
producer headers.

Unknown ingest IDs and IDs owned by another assignment MUST produce the same
HTTP 404 `ingest_not_found` representation: the same code, status, title,
detail, and `retryable=false`, with only normal per-request identifiers
differing. The service MUST NOT return 403, ownership hints, or observably
different lookup behavior for foreign IDs.

Status is not a delivery receipt and does not promise ordering, latency, or
exactly-once processing.

## 7. Bounded problem details

Errors use `application/problem+json` and the closed RFC 9457 extension schema
in `schemas/problem-details.schema.json`. `detail` is bounded and MUST NOT
include secrets, signatures, raw headers, or payload excerpts.

| HTTP | Stable code | Retry rule |
| --- | --- | --- |
| 400 | `malformed_request` | Correct the request; do not retry unchanged. |
| 401 | `authentication_failed` | Correct/rotate credentials or clock; do not retry unchanged. |
| 403 | `assignment_forbidden` | Obtain an authorized assignment; do not retry unchanged. |
| 404 | `ingest_not_found` | Do not retry automatically; unknown and foreign IDs are indistinguishable. |
| 409 | `replay_detected` | Retry only with a fresh timestamp, nonce, and signature. |
| 409 | `idempotency_conflict` | Do not reuse the key with a different body. |
| 409 | `idempotency_metadata_conflict` | Do not reuse the key with changed stable headers. |
| 409 | `idempotency_expired` | The key is tombstoned; start a new logical operation with a new key. |
| 413 | `payload_too_large` | Do not retry the same body. A smaller logical event needs a new key. |
| 415 | `unsupported_content_encoding` | Remove content encoding; do not retry unchanged. |
| 422 | `body_hash_mismatch` | Recompute the hash and signature; do not retry unchanged. |
| 422 | `invalid_event` | Correct event identifiers, media type, or route; do not retry unchanged. |
| 429 | `ingest_throttled` | Retry after `Retry-After` with the same key/body and fresh replay fields. |
| 503 | `ingest_unavailable` | Retry after `Retry-After` with the same key/body and fresh replay fields. |

Every response includes `Hookship-Request-Id`. HTTP 429 and 503 MUST include a
bounded `Retry-After` delta-seconds value. HTTP 413 SHOULD include
`Hookship-Max-Body-Bytes`. Clients MUST apply bounded exponential backoff with
jitter and MUST NOT infer a service-level commitment from `Retry-After`.

The problem schema is closed and binds each `code` to one HTTP `status` and
`retryable` value. Every status-specific OpenAPI fragment independently
requires the complete bounded RFC 9457 object and rejects unknown fields.
Adding a problem code requires a new HTTP major version.
