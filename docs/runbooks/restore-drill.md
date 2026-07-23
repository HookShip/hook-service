# Restore drill

A backup is only real once you have restored it. `restore-drill.sh` proves the
backup/restore path end to end on disposable databases, leaving your production
data untouched, and prints evidence you can archive.

## What it does

[`restore-drill.sh`](../../deploy/scripts/restore-drill.sh):

1. starts a disposable PostgreSQL "primary" container and migrates it;
2. backs it up with [`backup.sh`](../../deploy/scripts/backup.sh);
3. starts a fresh disposable "target" container and restores the backup with
   [`restore.sh`](../../deploy/scripts/restore.sh);
4. re-runs migrations on the target to confirm there is no schema drift;
5. verifies the restored schema (migration count and core tables);
6. tears everything down and prints a PASS/FAIL evidence block.

## Requirements

- Docker;
- `pg_dump`/`pg_restore`/`psql` matching the PostgreSQL 17+ server major version;
- an installed `hookshipctl`, or a Go toolchain to run `./cmd/hookshipctl` from a
  checkout.

## Run

```sh
deploy/scripts/restore-drill.sh
```

Example evidence:

```text
----- restore drill evidence -----
primary image:        postgres:17-alpine@sha256:...
backup dir:           /tmp/tmp.XXXX/backups/hookship-<stamp>
schema_migrations:    3 (expected 3)
core tables present:  5 (expected 5)
result:               PASS
----------------------------------
```

A non-zero exit or a `FAIL` line means the backup could not be restored into a
clean database — investigate before you rely on the backups. Use `--keep` to
leave the containers up for inspection.

## Recommended cadence

Run the drill on a schedule (for example weekly) and after any change to the
backup tooling, the migration set, or the PostgreSQL version. Keep the evidence
output with your operational records.
