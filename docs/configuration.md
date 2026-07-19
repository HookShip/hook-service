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

Validate configuration without starting the process:

```sh
HOOKSHIP_READ_TIMEOUT=15s ./bin/hookshipd -check-config
```

Binding a wildcard address is explicit. It should only be used behind a
deployment boundary that supplies the network access controls and transport
security appropriate to that environment.

## Durability integration tests

The durability repository is not wired into `hookshipd` in this phase, so
there is no runtime database variable yet. Integration tests use only:

| Variable | Default | Constraint |
| --- | --- | --- |
| `TEST_DATABASE_URL` | none | PostgreSQL 17+ connection URL for a disposable database; required only by `make test-integration` |

See [`examples/durability.env.example`](examples/durability.env.example). The
example contains no password. Supply test credentials through the local
environment or CI secret/service configuration, never source control.
