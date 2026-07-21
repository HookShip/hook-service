# 0004 — Standard Webhooks outbound signing

Status: accepted (pre-release).

## Context

Receivers need an interoperable, verifiable signature over the exact delivered
bytes.

## Decision

Outbound deliveries are signed with the Standard Webhooks scheme:
`webhook-id`, `webhook-timestamp`, and `webhook-signature: v1,<base64>` where
the signature is `HMAC-SHA256(secret, "<id>.<timestamp>.<payload>")`. The
signing secret is scoped to one immutable endpoint/assignment configuration and
is never logged. `whsec_`-prefixed secrets are base64-decoded. Deterministic
positive and negative vectors live in `internal/signing/testdata`.

## Consequences

Any Standard Webhooks-compatible receiver library can verify HookShip
deliveries. Inbound producer authentication remains the separate protocol-v1
HMAC scheme in `spec/v1/signing.md`.
