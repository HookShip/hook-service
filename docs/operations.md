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
