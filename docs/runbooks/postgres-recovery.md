# PostgreSQL recovery

PostgreSQL is the source of truth and the acceptance barrier. If it is lost, the
service cannot accept or deliver until it is recovered.

## Principles

- Recover from a [backup](backup-restore.md) into a PostgreSQL 17+ instance.
- Restoring an older schema is fine; the forward-only migrator brings it current
  when the schema is behind, and fails closed if the code is behind the schema
  (see [upgrade and rollback](upgrade-rollback.md)).
- The service does not exactly-once deliver; a restore may replay work that was
  in flight. Downstream idempotency (which this service already requires) makes
  replays harmless.

## Recover a lost database

```sh
sudo systemctl stop hookshipd.service

# Recreate an empty database/role, then restore the newest good backup:
sudo deploy/scripts/restore.sh \
  --input /var/lib/hookship/backups/hookship-<stamp> \
  --dsn "$HOOKSHIP_DATABASE_URL"

# Confirm the schema is current, then start serving:
sudo systemctl start hookship-migrate.service hookshipd.service
hookshipctl diagnostics -url http://127.0.0.1:8080
```

`restore.sh` verifies checksums before touching the database and runs
`pg_restore --clean`, so target an empty or disposable instance.

## Diagnose without a full restore

```sh
# Readiness reflects backend health and migration state:
curl -fsS http://127.0.0.1:8080/readyz

# Migration ledger and drift:
psql "$HOOKSHIP_DATABASE_URL" -c "SELECT version, name FROM schema_migrations ORDER BY version"
hookshipctl migrate   # idempotent; reports drift, applies anything missing
```

If `hookshipctl migrate` reports "database contains forward migration … missing
from source", the binary is older than the schema — deploy the matching (newer)
binary rather than downgrading the database.

## Connectivity and TLS

Use `sslmode=require` (or `verify-full`) in `HOOKSHIP_DATABASE_URL` when the
database is not on the VM's loopback. For local administration over SSH, tunnel
the database port to your workstation loopback:

```sh
ssh -N -L 5432:127.0.0.1:5432 operator@vm
psql "postgres://hookship@127.0.0.1:5432/hookship?sslmode=disable"
```

## After recovery

Run a [restore drill](restore-drill.md) to reconfirm the backup path, and verify
`/readyz` reports `ready` with the expected durability.
