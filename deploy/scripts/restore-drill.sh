#!/usr/bin/env bash
#
# restore-drill.sh — prove a backup can be restored, end to end, against
# disposable PostgreSQL instances. It creates a throwaway "primary" database,
# migrates it, backs it up with backup.sh, restores into a fresh throwaway
# "target" with restore.sh, re-runs migrations to confirm no schema drift, and
# verifies the restored schema. Everything is torn down afterward.
#
# This is a recovery-confidence drill: run it periodically and keep the output
# as evidence that the backup/restore path works.
#
# Usage:
#   deploy/scripts/restore-drill.sh [--keep] [--image REF]
#
#   --keep        Do not tear down the containers/temp dir (for debugging).
#   --image REF   PostgreSQL image (default the pinned postgres:17-alpine digest).
#
# Requires docker, pg_dump/pg_restore/psql, and either an installed hookshipctl
# or a Go toolchain (to run ./cmd/hookshipctl from the repository).
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
REPO_ROOT="$(cd -- "${SCRIPT_DIR}/../.." && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

PG_IMAGE="postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193"
KEEP="no"
PGPASS="drill_only"
SUFFIX="$$-$(date +%s)"
PRIMARY="hookship-drill-primary-${SUFFIX}"
TARGET="hookship-drill-target-${SUFFIX}"
WORKDIR=""

while [ "$#" -gt 0 ]; do
	case "$1" in
	--keep) KEEP="yes"; shift ;;
	--image) PG_IMAGE="${2:?--image requires a value}"; shift 2 ;;
	-h | --help) sed -n '2,22p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

hs_require docker pg_dump pg_restore psql

# Invoked indirectly via `trap cleanup EXIT`.
# shellcheck disable=SC2329
cleanup() {
	[ "${KEEP}" = "yes" ] && { hs_warn "--keep set; leaving ${PRIMARY}, ${TARGET}, ${WORKDIR}"; return; }
	docker rm -f "${PRIMARY}" "${TARGET}" >/dev/null 2>&1 || true
	[ -n "${WORKDIR}" ] && rm -rf "${WORKDIR}" || true
}
trap cleanup EXIT

run_migrate() {
	local dsn="$1"
	if command -v hookshipctl >/dev/null 2>&1; then
		HOOKSHIP_BACKEND=postgres HOOKSHIP_DATABASE_URL="${dsn}" hookshipctl migrate
	else
		hs_require go
		( cd "${REPO_ROOT}" && HOOKSHIP_BACKEND=postgres HOOKSHIP_DATABASE_URL="${dsn}" go run ./cmd/hookshipctl migrate )
	fi
}

start_pg() {
	local name="$1"
	docker run -d --name "${name}" \
		-e POSTGRES_DB=hookship -e POSTGRES_USER=hookship -e POSTGRES_PASSWORD="${PGPASS}" \
		-p 127.0.0.1::5432 "${PG_IMAGE}" >/dev/null
	local hostport
	hostport="$(docker port "${name}" 5432/tcp | head -n1)"
	local attempt=0
	while [ "${attempt}" -lt 30 ]; do
		if docker exec "${name}" pg_isready -U hookship -d hookship >/dev/null 2>&1; then break; fi
		attempt=$((attempt + 1))
		sleep 1
	done
	printf 'postgres://hookship:%s@%s/hookship?sslmode=disable' "${PGPASS}" "${hostport}"
}

WORKDIR="$(mktemp -d)"

hs_log "starting disposable primary (${PG_IMAGE%%@*})"
DSN_PRIMARY="$(start_pg "${PRIMARY}")"
hs_log "migrating primary"
run_migrate "${DSN_PRIMARY}" >/dev/null

hs_log "backing up primary"
BACKUP_DIR="$("${SCRIPT_DIR}/backup.sh" --dsn "${DSN_PRIMARY}" --output "${WORKDIR}/backups" --env-file /nonexistent | tail -n1)"
[ -d "${BACKUP_DIR}" ] || hs_die "backup did not produce a directory"

hs_log "starting disposable target"
DSN_TARGET="$(start_pg "${TARGET}")"
hs_log "restoring backup into target"
"${SCRIPT_DIR}/restore.sh" --input "${BACKUP_DIR}" --dsn "${DSN_TARGET}" --env-file /nonexistent --yes >/dev/null

hs_log "re-running migrations on target to confirm no drift"
run_migrate "${DSN_TARGET}" >/dev/null

hs_log "verifying restored schema"
migrations_count="$(psql "${DSN_TARGET}" -tAc "SELECT count(*) FROM schema_migrations")"
tables_count="$(psql "${DSN_TARGET}" -tAc "SELECT count(*) FROM information_schema.tables WHERE table_schema='public' AND table_name IN ('event_ingests','deliveries','durable_jobs','payload_records','nonce_ledger')")"

expected_migrations="$(find "${REPO_ROOT}/internal/durability/postgres/migrations" -name '*.up.sql' | wc -l | tr -d ' ')"

printf '\n----- restore drill evidence -----\n'
printf 'primary image:        %s\n' "${PG_IMAGE}"
printf 'backup dir:           %s\n' "${BACKUP_DIR}"
printf 'schema_migrations:    %s (expected %s)\n' "${migrations_count}" "${expected_migrations}"
printf 'core tables present:  %s (expected 5)\n' "${tables_count}"

if [ "${migrations_count}" = "${expected_migrations}" ] && [ "${tables_count}" = "5" ]; then
	printf 'result:               PASS\n'
	printf '%s\n' '----------------------------------'
	exit 0
fi
printf 'result:               FAIL\n'
printf '%s\n' '----------------------------------'
exit 1
