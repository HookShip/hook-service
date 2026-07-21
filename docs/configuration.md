# Configuration

`hookshipd` reads configuration from environment variables and rejects invalid
values before opening a listener.

| Variable | Default | Constraint |
| --- | --- | --- |
| `HOOKSHIP_LISTEN_ADDRESS` | `127.0.0.1:8080` | Valid `host:port`; port `0` is allowed for ephemeral evaluation |
| `HOOKSHIP_SHUTDOWN_TIMEOUT` | `10s` | Positive Go duration, at most `5m` |
| `HOOKSHIP_READ_HEADER_TIMEOUT` | `5s` | Positive Go duration, at most `1m` |
| `HOOKSHIP_READ_TIMEOUT` | `10s` | Positive Go duration, at most `10m` |
| `HOOKSHIP_WRITE_TIMEOUT` | `10s` | Positive Go duration, at most `10m` |
| `HOOKSHIP_IDLE_TIMEOUT` | `60s` | Positive Go duration, at most `1h` |
| `HOOKSHIP_MAX_HEADER_BYTES` | `65536` | Between 4096 and 1048576 bytes |
| `HOOKSHIP_LOG_LEVEL` | `info` | `debug`, `info`, `warn`, or `error` |

Durations use Go syntax such as `500ms`, `10s`, or `2m`.

## Runtime configuration

Selecting a backend enables the delivery runtime. The selection is validated
against the backend capability schema before a listener opens; invalid
combinations fail with a stable reason code (see
[`docs/backends.md`](backends.md)).

| Variable | Default | Constraint |
| --- | --- | --- |
| `HOOKSHIP_BACKEND` | empty (health-only) | `memory`, `postgres`, `postgres-redis`, or `postgres-kafka` |
| `HOOKSHIP_EVALUATION_MODE` | `false` | Required `true` for `memory`; loopback, non-production only |
| `HOOKSHIP_DATABASE_URL` | empty | Required by durable profiles; PostgreSQL 17+ URL |
| `HOOKSHIP_PAYLOAD_KEY` | empty | Required by durable profiles; 32-byte key in hex or base64 |
| `HOOKSHIP_PAYLOAD_BACKEND` | empty | `memory`, `postgres` (default durable), or `object-store` (experimental, not wired: the runtime keeps using the default store and writes no object-store bytes; hard-blocked in production) |
| `HOOKSHIP_REDIS_URL` | empty | Required by `postgres-redis` |
| `HOOKSHIP_KAFKA_BROKERS` | empty | Comma-separated brokers; required by `postgres-kafka` |
| `HOOKSHIP_CONFIG_FILE` | empty | Assignment config (YAML/JSON); required by durable profiles |
| `HOOKSHIP_WORKER_CONCURRENCY` | `4` | Between 1 and 256 |
| `HOOKSHIP_WORKER_LEASE` | `30s` | Between `1s` and `1h` |
| `HOOKSHIP_DELIVERY_TIMEOUT` | `30s` | Between `1s` and `10m` |
| `HOOKSHIP_MAX_ATTEMPTS` | `12` | Between 1 and 1000 |
| `HOOKSHIP_OUTBOX_SINK` | empty | `log`, a file path, or empty (log in evaluation, else noop) |
| `HOOKSHIP_METADATA_SINK_URL` | empty | Authenticated platform metadata-ingest endpoint |
| `HOOKSHIP_METADATA_SINK_TOKEN` | empty | Bearer token for the metadata sink |
| `HOOKSHIP_SINK_URL` | loopback default | Delivery target for the default evaluation assignment |
| `HOOKSHIP_PROXY_MODE` | `false` | Assert a trusted boundary terminates external exposure |
| `HOOKSHIP_TLS_CERT` / `HOOKSHIP_TLS_KEY` | empty | Direct HTTPS termination by the process. Both must be set together (a partial pair is rejected) and the pair is loaded at startup (a bad pair fails fast). When set, `/healthz`, `/readyz`, and ingest are served over HTTPS only. |
| `HOOKSHIP_DELIVERY_ALLOW_LOOPBACK` | `false` | Permit delivery to loopback sinks (reduces SSRF protection) |
| `HOOKSHIP_DELIVERY_ALLOW_PRIVATE` | `false` | Permit delivery to private/container sinks (reduces SSRF protection) |
| `HOOKSHIP_DELIVERY_ALLOW_PLAINTEXT` | `false` | Permit plaintext `http` delivery |

Non-loopback plaintext ingest fails closed unless TLS or `HOOKSHIP_PROXY_MODE`
is set. There are two mutually exclusive ways to secure a non-loopback listener:

- **Direct TLS:** set `HOOKSHIP_TLS_CERT` and `HOOKSHIP_TLS_KEY` (both, or
  neither). The process then terminates HTTPS itself; health, readiness, and
  ingest are served over HTTPS only, so probes and `hookshipctl diagnostics`
  must use an `https://` URL.
- **Trusted proxy:** set `HOOKSHIP_PROXY_MODE=true` when a loopback-only Docker
  publish, SSH tunnel, or reverse proxy terminates TLS upstream. The process
  serves plaintext behind that asserted boundary.

Loopback evaluation listeners continue to serve plaintext `http`. Delivery to
loopback/private/plaintext sinks requires the explicit
`HOOKSHIP_DELIVERY_ALLOW_*` opt-ins and is intended only for trusted local/VM
networks. The assignment config schema is in
[`../examples/compose/assignments.yaml`](../examples/compose/assignments.yaml).

Validate configuration without starting the process:

```sh
HOOKSHIP_READ_TIMEOUT=15s ./bin/hookshipd -check-config
```

Binding a wildcard address is explicit. It should only be used behind a
deployment boundary that supplies the network access controls and transport
security appropriate to that environment.

## Durability integration tests

The durable profiles use PostgreSQL at runtime via `HOOKSHIP_DATABASE_URL`.
Integration tests use a disposable database:

| Variable | Default | Constraint |
| --- | --- | --- |
| `TEST_DATABASE_URL` | none | PostgreSQL 17+ connection URL for a disposable database; required by `make test-integration` |
| `REDIS_URL` | none | Optional; enables the Redis relay integration test |
| `KAFKA_BROKERS` | none | Optional; enables the Kafka relay integration test |

See [`examples/durability.env.example`](examples/durability.env.example). The
example contains no password. Supply test credentials through the local
environment or CI secret/service configuration, never source control.
