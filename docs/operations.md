# Operations

## Probes

`GET` and `HEAD` are accepted on both operational endpoints. Responses are
JSON, non-cacheable, and include `status`, `service`, and `version`.

| Endpoint | Ready state | Unavailable state |
| --- | --- | --- |
| `/healthz` | `200`, status `ok` | Reserved for inability to serve the process probe |
| `/readyz` | `200`, status `ready` | `503`, status `not_ready` |

These endpoints report process state only. They do not assert successful
webhook delivery, durable storage, external dependency health, availability,
or an SLA.

## Diagnostics

```sh
hookshipctl diagnostics \
  -url=http://127.0.0.1:8080 \
  -timeout=3s
```

Use `-output=json` for machine-readable output. Diagnostics does not follow
redirects and bounds both request time and response parsing.

## Runtime modes

One binary exposes explicit roles:

```sh
hookshipd api      # ingest and status only
hookshipd worker   # dispatch, retry, DLQ, and metadata outbox
hookshipd all      # single-process developer/VM mode
```

Operator commands:

```sh
hookshipctl migrate            # apply PostgreSQL durability migrations
hookshipctl doctor             # validate configuration and backend selection
hookshipctl backend list       # list profiles and capabilities
hookshipctl backend describe postgres
hookshipctl backend validate -config hookship.yaml
hookshipd capabilities -output=json
```

`/readyz` reports the profile durability (`ephemeral`, `disk`, or `replicated`)
and fails closed when the backend is unreachable.

## Transport security

The listener serves plaintext `http` by default and is safe only on loopback.
For a non-loopback listener choose exactly one:

- Set `HOOKSHIP_TLS_CERT` and `HOOKSHIP_TLS_KEY` (both together) so the process
  terminates HTTPS directly. The pair is loaded at startup and a missing or
  partial pair fails fast. All endpoints — including `/healthz`, `/readyz`, and
  the container healthcheck — are then HTTPS only, so `hookshipctl diagnostics`
  must be pointed at an `https://` URL.
- Set `HOOKSHIP_PROXY_MODE=true` when TLS is terminated upstream (loopback-only
  Docker publish, SSH tunnel, or reverse proxy). The process serves plaintext
  behind that operator-asserted boundary.

Configuration validation rejects a non-loopback plaintext listener that has
neither TLS nor proxy mode, and rejects a partial certificate/key pair.

## Ingest and status

The producer data plane is `POST /v1/event-ingests` and
`GET /v1/event-ingests/{id}`, authenticated with the protocol-v1 exact-body
HMAC scheme in [`../spec/v1/signing.md`](../spec/v1/signing.md). Acceptance is
idempotent and durable for durable profiles; the ephemeral `memory` profile
marks every response `durability: "ephemeral"`. Status is payload-free and
returns the same `404 ingest_not_found` for unknown and foreign IDs. Errors are
bounded RFC 9457 `application/problem+json`.

## Migrations

Durable profiles require the schema to be applied before serving:

```sh
HOOKSHIP_BACKEND=postgres HOOKSHIP_DATABASE_URL=... hookshipctl migrate
```

Migrations are forward-only with immutable checksums. Readiness fails on
migration drift. The Compose durable profiles run a one-shot `migrate` service
before the `api` and `worker` services start.

## Delivery demo

```sh
scripts/demo.sh memory            # ephemeral, no PostgreSQL
scripts/demo.sh evaluation        # durable PostgreSQL
scripts/demo.sh evaluation retry  # exercise retry then success
```

The demo submits a signed event, delivers it (Standard Webhooks signature
verified by the local sink), and reads payload-free status.

## Shutdown

`SIGINT` and `SIGTERM` stop readiness and initiate graceful HTTP shutdown.
The shutdown duration is bounded by `HOOKSHIP_SHUTDOWN_TIMEOUT`. Operators
should configure their process supervisor or orchestrator with a termination
grace period longer than that value.

## Logging

`hookshipd` writes JSON process lifecycle and server error events to standard
error. There is no HTTP request or payload logging. Deployment layers should
also avoid capturing sensitive webhook payloads when delivery functionality is
introduced in a later phase.
