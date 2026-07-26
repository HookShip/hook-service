# Single-VM deployment

This directory packages `hook-service` for one Linux VM. Two deployment paths
are provided; both run the durable single-cell PostgreSQL profile with the
combined `api`+`worker` process (`hookshipd all`).

| Path | Runs | Use when |
| --- | --- | --- |
| **systemd** | Native binaries under a hardened systemd unit | You manage PostgreSQL separately and want a native service |
| **Compose (build)** | Containers built from source | You want an all-in-one stack including PostgreSQL, built locally |
| **Compose (pull)** | Containers from a published, immutable image digest | You deploy a released image |

Everything here is loopback-first, invents no registry or domain, and never
stores a credential in source control. The full operator reference is in
[`../docs/operations.md`](../docs/operations.md) and
[`../docs/configuration.md`](../docs/configuration.md).

## systemd path

```sh
# From a checkout on the VM (builds binaries if Go is present, else pass --bindir):
sudo deploy/scripts/install.sh
# Edit the generated config, then start:
sudoedit /etc/hookship/hookshipd.env          # replace every REPLACE_ME
sudoedit /etc/hookship/assignments.yaml
sudo systemctl enable --now hookship-migrate.service hookshipd.service
```

The installer:

- validates prerequisites (Linux + systemd, required tools);
- creates the non-root `hookship` system user and group;
- creates `/etc/hookship` (`0750`) and `/var/lib/hookship` (`0750`);
- installs `/etc/hookship/hookshipd.env` as **mode `0600`** and
  `/etc/hookship/assignments.yaml` as `0640`, owned by `hookship`;
- installs the binaries and the hardened units
  ([`hookship-migrate.service`](systemd/hookship-migrate.service) runs once
  before [`hookshipd.service`](systemd/hookshipd.service)), plus the opt-in
  [`hookship-backup.service`](systemd/hookship-backup.service) and
  [`hookship-backup.timer`](systemd/hookship-backup.timer) and the backup
  scripts under `/usr/local/lib/hookship/`;
- when the configuration is complete (no `REPLACE_ME` left), applies migrations,
  starts the service, and waits for `/readyz`. It refuses to start while any
  `REPLACE_ME` remains.

Pass `--enable-backups` (or run
`sudo systemctl enable --now hookship-backup.timer` later) to schedule the daily
backup; the units are installed either way. See
[backup and restore](../docs/runbooks/backup-restore.md).

`--prefix DIR` (default `/usr/local`) is honored for every unit: the installer
renders each unit's `ExecStart` from the selected prefix (binaries under
`DIR/bin`, backup scripts under `DIR/lib/hookship`), so a non-default prefix is
fully supported rather than silently pointing units at `/usr/local`.

Safe teardown keeps config, state, and the database; `--purge` removes them:

```sh
sudo deploy/scripts/uninstall.sh            # stop, disable, remove units + binaries
sudo deploy/scripts/uninstall.sh --purge    # also remove /etc/hookship, state, user
```

## Compose paths

```sh
# Source build (bundles PostgreSQL):
deploy/scripts/compose-up.sh build

# Pull a published, immutable digest (set HOOKSHIP_IMAGE=…@sha256:… in .env):
deploy/scripts/compose-up.sh pull

# Tear down:
deploy/scripts/compose-up.sh build down -v
```

`compose-up.sh` copies [`compose/.env.example`](compose/.env.example) and the
assignment example on first run, refuses to start while any `REPLACE_ME`
remains, and — for the pull path — **requires `HOOKSHIP_IMAGE` to be an
immutable `…@sha256:<64-hex>` digest**, rejecting mutable tags. The published
digest comes from the release workflow; this repository asserts no registry or
tag of its own.

## Transport security

The listener is plaintext and loopback-safe by default. To expose the service
off-host, choose exactly one of direct TLS (`HOOKSHIP_TLS_CERT` +
`HOOKSHIP_TLS_KEY`) or a trusted proxy (`HOOKSHIP_PROXY_MODE=true` behind a
reverse proxy or SSH tunnel). Operator status (`/statusz`) and metrics
(`/metrics`) are served on a separate, loopback-only operational listener
(`HOOKSHIP_OPERATIONAL_LISTEN_ADDRESS`, default `127.0.0.1:9090`) and never on
the ingest listener. See [`../docs/operations.md`](../docs/operations.md).

## Operational runbooks

Backup, restore, upgrade, rollback, key rotation, recovery, log rotation, and a
restore drill are provided as scripts in [`scripts/`](scripts) with matching
runbooks under [`../docs/runbooks/`](../docs/runbooks/).
