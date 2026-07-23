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

`rollback.sh` performs a **schema-compatible** rollback. It derives the actual
Compose project network (rather than assuming a fixed name), deploys the
previous version, and probes compatibility by running that version's
`hookshipctl migrate`, which fails closed if the database is ahead of the code.
The probe distinguishes three outcomes: schema-compatible (proceed),
schema-incompatible (refuse and point at a restore), and "could not run the
probe" (an unreachable database/network — it refuses without falsely claiming
incompatibility).

```sh
# systemd: redeploy the snapshotted previous binaries, probe, restart
sudo deploy/scripts/rollback.sh --mode systemd

# Compose: pin the previous immutable digest, probe, restart
deploy/scripts/rollback.sh --mode pull --image ghcr.io/<owner>/<repo>@sha256:<prev-digest>

# Dry-run only the network derivation + compatibility probe (installs/restarts nothing):
deploy/scripts/rollback.sh --mode systemd --check-only
deploy/scripts/rollback.sh --mode pull --image ghcr.io/<owner>/<repo>@sha256:<prev-digest> --check-only
```

Use `--check-only` before a real rollback to confirm the previous version is
schema-compatible without touching the running service.

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
