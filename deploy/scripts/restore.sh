#!/usr/bin/env bash
#
# restore.sh — restore a hook-service backup produced by backup.sh.
#
# Verifies the backup checksums, restores the PostgreSQL dump with pg_restore,
# and — when the backup contains an object-store mirror and mc is configured —
# mirrors the ciphertext objects back. PostgreSQL is the source of truth, so the
# database is restored first.
#
# Usage:
#   deploy/scripts/restore.sh --input DIR [--dsn DSN] [--env-file FILE] [--yes]
#
#   --input DIR     A backup directory created by backup.sh (required).
#   --dsn DSN       Target PostgreSQL DSN (default from env/env-file).
#   --env-file FILE Deployment env file (default /etc/hookship/hookshipd.env).
#   --yes           Do not prompt before overwriting the target database.
#
# WARNING: pg_restore runs with --clean, dropping and recreating objects in the
# target database. Point --dsn at the intended recovery target. Stop hookshipd
# first so nothing writes during the restore.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

INPUT=""
DSN_ARG=""
ENV_FILE="${HOOKSHIP_ENV_FILE:-/etc/hookship/hookshipd.env}"
ASSUME_YES="no"

while [ "$#" -gt 0 ]; do
	case "$1" in
	--input) INPUT="${2:?--input requires a value}"; shift 2 ;;
	--dsn) DSN_ARG="${2:?--dsn requires a value}"; shift 2 ;;
	--env-file) ENV_FILE="${2:?--env-file requires a value}"; shift 2 ;;
	--yes) ASSUME_YES="yes"; shift ;;
	-h | --help) sed -n '2,24p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

export HOOKSHIP_ENV_FILE="${ENV_FILE}"
hs_require pg_restore
[ -n "${INPUT}" ] || hs_die "--input DIR is required"
[ -d "${INPUT}" ] || hs_die "backup directory not found: ${INPUT}"
[ -f "${INPUT}/db.dump" ] || hs_die "no db.dump in ${INPUT}"

DSN="$(hs_resolve_dsn "${DSN_ARG}")"
[ -n "${DSN}" ] || hs_die "no PostgreSQL DSN (pass --dsn or set HOOKSHIP_DATABASE_URL / ${ENV_FILE})"

if [ -f "${INPUT}/SHA256SUMS" ]; then
	hs_log "verifying backup integrity"
	( cd "${INPUT}" && hs_sha256_check SHA256SUMS >/dev/null ) || hs_die "checksum verification failed for ${INPUT}"
else
	hs_warn "no SHA256SUMS in ${INPUT}; skipping integrity check"
fi

if [ "${ASSUME_YES}" != "yes" ]; then
	printf 'Restore into %s? This overwrites existing objects. [y/N] ' "$(hs_redact_dsn "${DSN}")"
	read -r reply
	case "${reply}" in y | Y | yes | YES) ;; *) hs_die "aborted" ;; esac
fi

hs_log "restoring PostgreSQL into $(hs_redact_dsn "${DSN}")"
pg_restore --clean --if-exists --no-owner --no-privileges --dbname "${DSN}" "${INPUT}/db.dump"
hs_log "database restore complete"

if [ -d "${INPUT}/objects" ]; then
	if command -v mc >/dev/null 2>&1 && [ -f "${ENV_FILE}" ]; then
		endpoint="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_ENDPOINT=//p' "${ENV_FILE}" | tail -n1)"
		bucket="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_BUCKET=//p' "${ENV_FILE}" | tail -n1)"
		access="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_ACCESS_KEY_ID=//p' "${ENV_FILE}" | tail -n1)"
		secret="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_SECRET_ACCESS_KEY=//p' "${ENV_FILE}" | tail -n1)"
		use_tls="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_USE_TLS=//p' "${ENV_FILE}" | tail -n1)"
		scheme="http"; [ "${use_tls}" = "true" ] && scheme="https"
		if [ -n "${endpoint}" ] && [ -n "${bucket}" ]; then
			hs_log "mirroring objects back into bucket ${bucket}"
			mc alias set hookship-restore "${scheme}://${endpoint}" "${access}" "${secret}" >/dev/null
			mc mb --ignore-existing "hookship-restore/${bucket}" >/dev/null 2>&1 || true
			mc mirror --overwrite "${INPUT}/objects" "hookship-restore/${bucket}"
			mc alias remove hookship-restore >/dev/null 2>&1 || true
		else
			hs_warn "object mirror present but object-store endpoint/bucket missing; skipping"
		fi
	else
		hs_warn "object mirror present but 'mc' or env file missing; restore objects manually from ${INPUT}/objects"
	fi
fi

hs_log "restore complete; start the migrator to confirm schema currency:"
hs_log "  hookshipctl migrate   (or: systemctl start hookship-migrate.service)"
