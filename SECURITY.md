# Security policy

## Reporting a vulnerability

Use GitHub's private vulnerability reporting for this repository when
available. Do not open a public issue for an unpatched vulnerability, and do
not include secrets, credentials, webhook payloads, or personal data in a
report.

Include the affected revision, impact, reproduction steps, and any suggested
mitigation. Maintainers will assess reports according to impact and available
project capacity; this document does not promise a response or remediation
timeline.

## Current scope

The repository implements the outbound delivery data plane: protocol-v1
authenticated ingest, idempotent durable acceptance on PostgreSQL, an
S3-compatible object-store payload backend, AES-256-GCM payload sealing with a
static or AWS-compatible KMS key provider, Standard Webhooks-compatible outbound
signing, and an SSRF-safe delivery worker with retries, DLQ, per-endpoint and
per-assignment limits, and retention workers.

Security-relevant properties intended to hold:

- No request bodies, payloads, secrets, or identifiers are logged; attempts,
  outbox rows, status, and the `/statusz` operator metrics are payload-free.
- Stored payload bytes are AES-256-GCM ciphertext only; the PostgreSQL ledger
  keeps object locators, hashes, and key metadata, never raw bytes.
- The delivery transport pins DNS, rejects private/link-local/metadata targets,
  refuses redirects, verifies TLS, and bounds timeouts and response reads.
- Non-loopback plaintext ingest and non-loopback plaintext object-store
  transport fail closed unless an operator asserts an explicit trusted boundary.

This is a pre-release (`v0.x`) project that has not been independently audited
and makes no availability or service-level claims. Security review should be
repeated before any production use. Report suspected weaknesses in these
properties through the process above.
