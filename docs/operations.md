# Operations

## Probes

`GET` and `HEAD` are accepted on both operational endpoints. Responses are
JSON, non-cacheable, and include `status`, `service`, and `version`.

`/healthz` and `/readyz` are served on **both** the ingest listener
(`HOOKSHIP_LISTEN_ADDRESS`) and the separate loopback operational listener
(`HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS`, default `127.0.0.1:9090`). The
operational surface — `/statusz` and `/metrics` — is served **only** on the
operational listener, never on the (potentially externally exposed) ingest
listener.

| Endpoint | Listener | Ready state | Unavailable state |
| --- | --- | --- | --- |
| `/healthz` | ingest + operational | `200`, status `ok` | Reserved for inability to serve the process probe |
| `/readyz` | ingest + operational | `200`, status `ready` | `503`, status `not_ready` |
| `/statusz` | operational only | `200`, payload-free operator status JSON | — |
| `/metrics` | operational only | `200`, OpenMetrics exposition | — |

These endpoints report process state only. They do not assert successful
webhook delivery, durable storage, external dependency health, availability,
or an SLA.

`/statusz` returns a payload-free operator snapshot: `service`, `version`,
`backend`, `durability`, `payloadBackend` (`postgres` or `object-store`),
`keyProvider` (`static` or `aws-kms`), and a `metrics` object of counters and
gauges (delivery outcomes, circuit-open skips, rate-limit/concurrency deferrals,
an in-flight gauge, and retention counts). It never contains request bodies,
payloads, secrets, or identifiers. Delivery and retention metrics are recorded
by the `worker`/`all` process; the `api` process reports its own registry.

## Metrics

`/metrics` on the operational listener serves the same payload-free counters and
gauges in the OpenMetrics text exposition format (a superset of the Prometheus
text format), so a standard scraper can consume them. Every series is namespaced
with the `hookship_` prefix; counters carry the `_total` suffix. Because the
operational listener is loopback-only, scrape it with an agent running on the
host (or over an SSH tunnel); it is never exposed off-box.

```sh
curl -fsS http://127.0.0.1:9090/metrics
```

## Retention and cleanup

The `worker` and `all` modes run a bounded retention worker
(`HOOKSHIP_RETENTION_INTERVAL`, default `1m`) that, in batches of
`HOOKSHIP_RETENTION_BATCH` (default `500`):

- purges expired replay-protection nonces;
- advances committed idempotency ledgers to tombstones once their retention
  window elapses;
- orphans object-store payload stages that expired before acceptance;
- reclaims expired payload ciphertext under a fencing lease — nulling PostgreSQL
  ciphertext bytes and deleting the immutable object-store objects (including
  stale stage-write attempts) so no ciphertext is left behind;
- purges terminal metadata-outbox rows older than `HOOKSHIP_OUTBOX_RETENTION`
  (default `24h`).

Every operation is bounded per pass and reported to `/statusz`. Payload
reclamation only runs once a payload's jobs are terminal and its idempotency
ledger is tombstoned, so in-flight deliveries are never affected.

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

`all` (the single-VM systemd deployment) runs one process, so the default
listeners are correct with no extra configuration. If you split `api` and
`worker` into **two processes on the same host**, give each its own listener
addresses — both the ingest listener and the loopback operational listener
default to the same ports, so the second process to start otherwise fails fast:

```sh
# api process
HOOKSHIP_LISTEN_ADDRESS=127.0.0.1:8080 \
HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS=127.0.0.1:9090 hookshipd api

# worker process (distinct ports on the same host)
HOOKSHIP_LISTEN_ADDRESS=127.0.0.1:8081 \
HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS=127.0.0.1:9091 hookshipd worker
```

The address-in-use error names the fix; there is no shared default that silently
works for two same-host processes.

Operator commands:

```sh
hookshipctl version            # print CLI/build version (text or -output=json)
hookshipctl migrate            # apply PostgreSQL durability migrations
hookshipctl doctor             # validate configuration and backend selection
hookshipctl diagnostics        # read a running daemon's /healthz and /readyz
hookshipctl backend list       # list profiles and capabilities
hookshipctl backend describe postgres
hookshipctl backend validate -config hookship.yaml
hookshipctl redrive list       # list payload-free dead-letter (DLQ) jobs
hookshipctl redrive run -assignment asg_orders --all   # explicit operator redrive
hookshipd capabilities -output=json
```

`hookshipctl redrive` is the supported dead-letter recovery path: it drives the
same runtime backend ports the worker uses (`ListDeadJobs`, `RedriveJob`) rather
than mutating rows directly, and prints only payload-free job metadata (ids,
attempt counts, error codes) — never payload bytes or secrets. It reads the same
`HOOKSHIP_BACKEND`/`HOOKSHIP_DATABASE_URL`/`HOOKSHIP_CONFIG_FILE` configuration
as the daemon.

`/readyz` reports the profile durability (`ephemeral`, `disk`, or `replicated`)
and fails closed when the backend is unreachable.

## Transport security

The listener serves plaintext `http` by default and is safe only on loopback.
For a non-loopback listener choose exactly one:

- Set `HOOKSHIP_TLS_CERT` and `HOOKSHIP_TLS_KEY` (both together) so the process
  terminates HTTPS directly. The pair is loaded at startup and a missing or
  partial pair fails fast. The ingest listener — including `/healthz`, `/readyz`,
  and the container healthcheck — is then HTTPS only, so `hookshipctl
  diagnostics` must be pointed at an `https://` URL. The loopback operational
  listener (`/statusz`, `/metrics`) stays plaintext.
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
error. There is no HTTP request or payload logging. Attempts, outbox rows,
status, and the `/statusz` operator metrics are payload-free; deployment layers
should likewise avoid capturing sensitive webhook payloads.
