# HMAC request signing v1

## Transport prerequisite

Signed requests MUST use HTTPS for every non-loopback destination, with TLS 1.2
or newer and TLS 1.3 preferred. Clients MUST disable redirects for signed
requests and MUST NOT retry an HTTPS request over plaintext. Servers MUST NOT
redirect signed plaintext requests to HTTPS because doing so can expose the
authorization and payload before the redirect.

Loopback HTTP is allowed only in explicitly enabled non-production evaluation
mode with credentials that cannot authenticate to production or non-loopback
assignments. HMAC integrity is not transport confidentiality.

## Credential

The credential is a random symmetric secret scoped to one assignment and the
`event.ingest` and/or `event.status` purpose. The credential identifier is not
the tenant, environment, or producer identity. The server resolves those from
the credential assignment.

The authorization field is:

```text
Authorization: Hookship-HMAC Credential=<credential-id>,Signature=<base64url>
```

The signature is unpadded base64url HMAC-SHA256 over the UTF-8 canonical
string. Servers MUST compare decoded signatures in constant time and MUST
return only the bounded `authentication_failed` problem for credential,
signature, or timestamp authentication failures.

The 32-byte HMAC has exactly 43 unpadded base64url characters and its final
character MUST be one of `[AEIMQUYcgkosw048]`. Verifiers MUST decode strictly
and require that re-encoding the decoded bytes produces the identical input
text, rejecting aliases with non-zero unused trailing bits.

`X-Hookship-Timestamp` MUST match exactly
`^[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}Z$` and represent a
real UTC instant. Fractional seconds, offsets including `+00:00`, lowercase
`z`, and omitted seconds are invalid.

## Canonical string

The canonical string has exactly these twelve LF-separated lines and no final
LF:

```text
hookship-hmac-sha256-v1
method:<uppercase HTTP method>
path:<exact origin-form path>
credential-id:<credential identifier>
timestamp:<X-Hookship-Timestamp>
nonce:<X-Hookship-Nonce>
idempotency-key:<Idempotency-Key or empty>
event-id:<X-Hookship-Event-ID or empty>
event-type:<X-Hookship-Event-Type or empty>
route:<X-Hookship-Route or empty>
content-type:<Content-Type field value or empty>
body-sha256:<lowercase hexadecimal SHA-256>
```

For event ingest, all values after `nonce` are non-empty. For status GET,
idempotency key, event ID, event type, route, and content type are empty, and
the body hash is SHA-256 of the empty byte sequence.

Header values are signed after removal of optional leading/trailing HTTP
whitespace. Embedded control characters, commas in `Authorization` parameter
values, duplicate signed headers, query strings, dot-segment paths, and
percent-encoded path aliases are rejected rather than normalized. `Content-Type`
is signed exactly as transmitted after outer whitespace removal; clients
should use a stable lowercase media type.

## Verification order

An implementation MUST bound work and disclosure. It SHOULD:

1. validate method, path, header count, syntax, timestamp window, and body
   limit;
2. resolve the scoped credential without exposing whether it exists;
3. stream and hash the exact body;
4. verify the declared body hash and HMAC;
5. reserve the replay nonce;
6. apply assignment authorization and idempotency;
7. cross the durability acknowledgement barrier.

If a nonce is reserved but processing later returns 503, the client uses a new
nonce while retaining its idempotency key and payload.

Deterministic accepted, duplicate, conflict, and status vectors are in
[`fixtures/signing-vectors.json`](fixtures/signing-vectors.json). Its
`secretHex` value is the obvious public, low-entropy 32-byte sequence
`00 01 ... 1f`; it is test material and MUST NOT be used outside tests.
Positive and negative timestamp cases are in
[`fixtures/timestamp-vectors.json`](fixtures/timestamp-vectors.json).
