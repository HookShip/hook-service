# Backup and restore

PostgreSQL is the source of truth. When the object-store payload backend is
enabled, the ciphertext objects are a second dataset that must be captured with
the database. [`backup.sh`](../../deploy/scripts/backup.sh) and
[`restore.sh`](../../deploy/scripts/restore.sh) do both.

## What a backup contains

A backup directory `hookship-<UTC-stamp>/` holds:

- `db.dump` — a `pg_dump` custom-format (compressed) snapshot of the database;
- `objects/` — a mirror of the ciphertext bucket, only when the object-store
  backend is configured and `mc` is installed;
- `SHA256SUMS` — checksums of every file, verified on restore;
- `manifest.json` — timestamp, redacted DSN, and the database dump checksum.

The database is dumped first, then objects are mirrored. Because object payloads
are immutable and are only reclaimed after they become terminal, every object a
consistent dump still references is captured; reclaimed objects are intentionally
absent because they are no longer needed.

## Prerequisites

- `pg_dump`/`pg_restore` from PostgreSQL client tools matching the server major
  version (the server requires PostgreSQL 17+). Install `postgresql-client-17`.
- `mc` (MinIO client) only if the object-store backend is enabled.
- A DSN: `--dsn`, `HOOKSHIP_DATABASE_URL`, or the deployment env file.

## Back up

```sh
# Uses the DSN and object-store settings from /etc/hookship/hookshipd.env:
sudo deploy/scripts/backup.sh --output /var/lib/hookship/backups
```

Schedule it (systemd timer or cron) and monitor freshness with
[`backup-age.sh`](../../deploy/scripts/backup-age.sh), which exits non-zero when
the newest backup is older than a threshold — usable as a health check that
produces backup-age evidence:

```sh
deploy/scripts/backup-age.sh --dir /var/lib/hookship/backups --max-age-hours 26
```

### Scheduled backups (systemd timer)

The systemd deployment ships an opt-in daily backup timer. The
`hookship-backup.service` unit runs `backup.sh` under the same hardened sandbox
as the main service (it may only read `/etc/hookship` and write
`/var/lib/hookship`), and `hookship-backup.timer` triggers it once a day with
jitter and catch-up on a missed run.

`deploy/scripts/install.sh` always installs both units (and the backup scripts
to `/usr/local/lib/hookship/`), but leaves the timer disabled unless you opt in:

```sh
# Enable at install time:
sudo deploy/scripts/install.sh --enable-backups

# Or enable later:
sudo systemctl enable --now hookship-backup.timer
systemctl list-timers hookship-backup.timer   # confirm the next run
sudo systemctl start hookship-backup.service   # run one backup now
```

Backups land in `/var/lib/hookship/backups`. Pair the timer with a periodic
`backup-age.sh` check (its own cron/timer or your monitoring) to alert when the
newest backup goes stale. Adjust the schedule by editing the `OnCalendar` line
in `/etc/systemd/system/hookship-backup.timer` (then `systemctl daemon-reload`).

## Restore

Stop the service first so nothing writes during the restore. `pg_restore` runs
with `--clean`, so point `--dsn` at the intended target.

```sh
sudo systemctl stop hookshipd.service
sudo deploy/scripts/restore.sh \
  --input /var/lib/hookship/backups/hookship-<stamp> \
  --dsn "$HOOKSHIP_DATABASE_URL"
sudo systemctl start hookship-migrate.service hookshipd.service
```

`restore.sh` verifies `SHA256SUMS` before touching the database, restores
`db.dump`, and — when `objects/` is present and `mc` is configured — mirrors the
ciphertext back into the bucket. Starting `hookship-migrate.service` afterward
confirms the schema is current.

## Compose deployments

The bundled PostgreSQL is not published on the host, so run the client inside the
container:

```sh
cd deploy/compose
docker compose -f compose.build.yaml exec -T postgres \
  pg_dump -U hookship -Fc hookship > db.dump
# restore:
docker compose -f compose.build.yaml exec -T postgres \
  pg_restore -U hookship --clean --if-exists -d hookship < db.dump
```

## Verify recoverability

Do not trust an untested backup. Run the [restore drill](restore-drill.md)
regularly against disposable databases.
