# Deployment (single VM)

This guide deploys `hook-service` to one Linux VM running the durable
single-cell PostgreSQL profile with the combined `api`+`worker` process
(`hookshipd all`). The packaging and scripts live in
[`../deploy`](../deploy/README.md); this page is the operator narrative.

The service is loopback-first, invents no registry or domain, and never stores a
credential in source control. It makes no availability or service-level claim.

## Choose a path

| Path | Runs | Choose when |
| --- | --- | --- |
| systemd | Native binaries under hardened units | You run PostgreSQL separately and want a native service |
| Compose (build) | Containers built from source (bundles PostgreSQL) | You want an all-in-one stack, built locally |
| Compose (pull) | Containers from a published immutable image digest | You deploy a released image |

## Prerequisites

- A Linux host with systemd (for the systemd path) or Docker + Docker Compose v2
  (for the Compose paths).
- PostgreSQL 17+ reachable via `HOOKSHIP_DATABASE_URL` (the Compose paths bundle
  it).
- A 32-byte payload key (`openssl rand -hex 32`) or the AWS-compatible KMS
  provider.
- Client tooling for backups (`postgresql-client-17`, and `mc` if using the
  object-store backend).

## systemd path

```sh
# On the VM, from a checkout (builds binaries if Go is present, else --bindir DIR):
sudo deploy/scripts/install.sh

# Fill in the generated, mode-0600 config, then start:
sudoedit /etc/hookship/hookshipd.env          # replace every REPLACE_ME
sudoedit /etc/hookship/assignments.yaml
sudo systemctl enable --now hookship-migrate.service hookshipd.service
```

The installer validates prerequisites, creates the non-root `hookship` user and
`0750` directories, installs the config `0600`/`0640`, installs the binaries and
the two hardened units, applies migrations, and waits for `/readyz`. It refuses
to start while any `REPLACE_ME` remains. See
[`../deploy/README.md`](../deploy/README.md) for `uninstall.sh` and the safe vs
`--purge` teardown.

## Compose paths

```sh
# Source build (bundles PostgreSQL):
deploy/scripts/compose-up.sh build

# Pull a published immutable digest (set HOOKSHIP_IMAGE=…@sha256:… in .env):
deploy/scripts/compose-up.sh pull
```

The pull path **requires an immutable `…@sha256:<64-hex>` digest** and rejects
mutable tags. The digest comes from the release workflow
([releasing](releasing.md)); this repository asserts no registry or tag.

## Transport security

The listener serves plaintext and is safe only on loopback. To expose it
off-host, choose exactly one:

- **Direct TLS** — set `HOOKSHIP_TLS_CERT` and `HOOKSHIP_TLS_KEY` (both) and bind
  a non-loopback address; probes then require `https://`.
- **Trusted proxy** — set `HOOKSHIP_PROXY_MODE=true` behind a reverse proxy or
  SSH tunnel that terminates TLS, and keep the process bound to loopback.

Configuration validation rejects a non-loopback plaintext listener with neither.
See [`configuration.md`](configuration.md) and [`operations.md`](operations.md).

## Verify

```sh
curl -fsS http://127.0.0.1:8080/readyz     # expect status: ready
hookshipctl diagnostics -url http://127.0.0.1:8080
hookshipctl doctor                          # validate backend selection

# Operator status and OpenMetrics live on the separate loopback operational
# listener (default 127.0.0.1:9090), never on the ingest listener:
curl -fsS http://127.0.0.1:9090/statusz
curl -fsS http://127.0.0.1:9090/metrics
```

Over SSH from a workstation, tunnel the loopback listener:

```sh
ssh -N -L 8080:127.0.0.1:8080 operator@vm
```

## Day-2 operations

Backup, restore, upgrade, schema-compatible rollback, key rotation, and recovery
are covered by the [operational runbooks](runbooks/README.md). Always take a
[backup](runbooks/backup-restore.md) before an [upgrade](runbooks/upgrade-rollback.md),
and periodically run the [restore drill](runbooks/restore-drill.md).
