# Upgrade and rollback

## Migration model

Migrations are **forward-only** with immutable checksums. `hookshipctl migrate`
and readiness both fail closed on drift, including when the database contains a
migration the running binary does not know
([`internal/durability/postgres/migrate.go`](../../internal/durability/postgres/migrate.go)).
This has a direct operational consequence:

> A binary/image rollback is safe **only** if the newer release added no
> migration. If the upgrade applied a new migration, the older code cannot serve
> that schema, and the correct recovery is a restore from the pre-upgrade backup.

## Upgrade

[`upgrade.sh`](../../deploy/scripts/upgrade.sh) always takes a pre-upgrade backup
first (unless `--no-backup`), then applies migrations and restarts with a
readiness gate.

```sh
# systemd: install new binaries, snapshot the old ones for rollback, migrate, restart
sudo deploy/scripts/upgrade.sh --mode systemd --bindir /path/to/new/bin

# Compose (source build):
deploy/scripts/upgrade.sh --mode build

# Compose (pull an immutable digest):
deploy/scripts/upgrade.sh --mode pull --image ghcr.io/<owner>/<repo>@sha256:<digest>
```

For the systemd path the current binaries are snapshotted to
`/var/lib/hookship/rollback` before the new ones are installed, so
[`rollback.sh`](../../deploy/scripts/rollback.sh) can redeploy them.

## Rollback

`rollback.sh` performs a **schema-compatible** rollback. It deploys the previous
version and probes compatibility by running that version's `hookshipctl migrate`,
which fails closed if the database is ahead of the code. If the probe fails, it
refuses to start the old version and tells you to restore instead.

```sh
# systemd: redeploy the snapshotted previous binaries, probe, restart
sudo deploy/scripts/rollback.sh --mode systemd

# Compose: pin the previous immutable digest, probe, restart
deploy/scripts/rollback.sh --mode pull --image ghcr.io/<owner>/<repo>@sha256:<prev-digest>
```

If the rollback is rejected as schema-incompatible:

```sh
sudo systemctl stop hookshipd.service
sudo deploy/scripts/restore.sh --input /var/lib/hookship/backups/hookship-<stamp> --dsn "$HOOKSHIP_DATABASE_URL"
# redeploy the previous binaries/image, then start the service
```

This restores both the previous schema and data from the pre-upgrade backup.

## Notes

- The image reference is parameterized; this repository asserts no registry or
  tag. Use the exact immutable `…@sha256:…` digest your release published.
- Readiness is the upgrade/rollback gate. If a deployment does not become ready,
  it did not upgrade — check `journalctl -u hookshipd` and the
  [troubleshooting guide](../troubleshooting.md).
