# PostgreSQL migrations

HookShip uses forward-only migrations for one regional cell database. Migration
files use `NNNNNN_name.up.sql`; rollback files are intentionally unsupported.

`checksums.txt` contains the immutable SHA-256 of every embedded migration. The
runner verifies the source manifest before connecting and compares the same
checksum with `schema_migrations` before applying later migrations. Editing an
applied migration is a hard error. Corrective changes require a new migration.

To add a migration:

1. create the next numbered `.up.sql` file;
2. compute `shasum -a 256 internal/durability/postgres/migrations/*.up.sql`;
3. add the exact hashes and filenames to `checksums.txt`;
4. run `make migration-check`;
5. run `make test-integration` against a disposable PostgreSQL 17+ database.

The migration role needs DDL rights during deployment. Runtime repositories
should use a narrower role after migration.
