# 0005 — SSRF-safe pinned-address transport

Status: accepted (pre-release).

## Context

Delivery targets are operator/tenant-supplied URLs and must not be usable to
reach internal, loopback, or cloud-metadata addresses.

## Decision

The delivery transport resolves DNS, validates the resolved IP, and dials that
exact pinned address (defeating DNS rebinding). Private, loopback, link-local,
unspecified, multicast, and cloud-metadata addresses are rejected by default.
Redirects are refused, TLS is verified, ambient proxies are ignored, all
timeouts are bounded, and the response body is drained and discarded under a
strict byte cap. Loopback, private, and plaintext delivery are explicit,
default-off opt-ins for trusted local/VM sinks.

## Consequences

Delivering to a local sink (as in the demos) requires an explicit allowance,
which the Compose profiles set on the trusted local network.
