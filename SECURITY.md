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

The repository currently exposes only process health/readiness endpoints and
diagnostic tooling. Webhook delivery, persistence, signing, queues, databases,
and cloud integrations are not implemented. Security review must be repeated
when the protocol and durability phase begins.
