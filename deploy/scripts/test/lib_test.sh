#!/usr/bin/env bash
#
# lib_test.sh — hermetic tests for the pure helpers in lib.sh (DSN construction,
# secret redaction, Compose network naming, and migrate-probe classification).
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"
# shellcheck source=deploy/scripts/lib.sh
. "${TEST_DIR}/../lib.sh"

# hs_pg_dsn builds a valid libpq URL DSN for a simple secret.
assert_eq \
	"postgres://hookship:drill_only@127.0.0.1:5432/hookship?sslmode=disable" \
	"$(hs_pg_dsn hookship drill_only 127.0.0.1:5432 hookship)" \
	"hs_pg_dsn simple"

# hs_pg_dsn percent-encodes a secret with URL-reserved characters so it cannot
# corrupt the surrounding DSN (secret-safe).
assert_eq \
	"postgres://hookship:p%40ss%3Aw%2Ford%3F@host:1/hookship?sslmode=disable" \
	"$(hs_pg_dsn hookship 'p@ss:w/ord?' host:1 hookship)" \
	"hs_pg_dsn encodes reserved chars"

# hs_pg_dsn defaults the database name to hookship.
assert_eq \
	"postgres://hookship:x@h:5432/hookship?sslmode=disable" \
	"$(hs_pg_dsn hookship x h:5432)" \
	"hs_pg_dsn default db"

# hs_redact_dsn hides the password but preserves the rest.
assert_eq \
	"postgres://hookship:REDACTED@127.0.0.1:5432/hookship?sslmode=disable" \
	"$(hs_redact_dsn "$(hs_pg_dsn hookship supersecret 127.0.0.1:5432 hookship)")" \
	"hs_redact_dsn"

# A redacted DSN never contains the secret.
assert_not_contains \
	"$(hs_redact_dsn "$(hs_pg_dsn hookship topsecret h:1 db)")" \
	"topsecret" \
	"redaction removes secret"

# hs_compose_default_network mirrors Docker Compose's <project>_default naming.
assert_eq "hookship-deploy_default" "$(hs_compose_default_network hookship-deploy)" "default network name"

# hs_probe_migrate_compat classification.
rc=0
hs_probe_migrate_compat true || rc=$?
assert_status 0 "${rc}" "probe compatible"

rc=0
hs_probe_migrate_compat sh -c 'echo "database contains forward migration 000009 (foo) missing from source" >&2; exit 1' 2>/dev/null || rc=$?
assert_status 1 "${rc}" "probe incompatible (missing from source)"

rc=0
hs_probe_migrate_compat sh -c 'echo "connection refused" >&2; exit 1' 2>/dev/null || rc=$?
assert_status 2 "${rc}" "probe could not run"

assert_done
