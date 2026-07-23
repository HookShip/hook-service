#!/usr/bin/env bash
#
# backup.sh — consistent backup of the hook-service durable state.
#
# Dumps PostgreSQL (the source of truth) with pg_dump in the compressed custom
# format and, when the object-store payload backend is configured, mirrors the
# ciphertext bucket. The database is dumped first so every object the dump
# references that is still live is captured by the subsequent mirror. Writes a
# manifest and a SHA256SUMS file that also serve as backup-age evidence.
#
# Usage:
#   deploy/scripts/backup.sh [--output DIR] [--dsn DSN] [--env-file FILE]
#
#   --output DIR    Parent directory for the timestamped backup (default
#                   /var/lib/hookship/backups, falling back to ./backups).
#   --dsn DSN       PostgreSQL DSN (default from HOOKSHIP_DATABASE_URL or the
#                   deployment env file).
#   --env-file FILE Deployment env file to read the DSN and object-store settings
#                   from (default /etc/hookship/hookshipd.env).
#
# Object-store mirroring uses `mc` (MinIO client) when installed and configured;
# it is skipped with a warning otherwise. Nothing is published anywhere.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

OUTPUT=""
DSN_ARG=""
ENV_FILE="${HOOKSHIP_ENV_FILE:-/etc/hookship/hookshipd.env}"

while [ "$#" -gt 0 ]; do
	case "$1" in
	--output) OUTPUT="${2:?--output requires a value}"; shift 2 ;;
	--dsn) DSN_ARG="${2:?--dsn requires a value}"; shift 2 ;;
	--env-file) ENV_FILE="${2:?--env-file requires a value}"; shift 2 ;;
	-h | --help) sed -n '2,24p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

export HOOKSHIP_ENV_FILE="${ENV_FILE}"
hs_require pg_dump

DSN="$(hs_resolve_dsn "${DSN_ARG}")"
[ -n "${DSN}" ] || hs_die "no PostgreSQL DSN (pass --dsn or set HOOKSHIP_DATABASE_URL / ${ENV_FILE})"

if [ -z "${OUTPUT}" ]; then
	if [ -d /var/lib/hookship ] && [ -w /var/lib/hookship ]; then
		OUTPUT="/var/lib/hookship/backups"
	else
		OUTPUT="./backups"
	fi
fi

STAMP="$(hs_timestamp)"
DIR="${OUTPUT}/hookship-${STAMP}"
mkdir -p "${DIR}"

hs_log "backing up PostgreSQL from $(hs_redact_dsn "${DSN}")"
pg_dump --format=custom --no-owner --no-privileges --dbname "${DSN}" --file "${DIR}/db.dump"
db_bytes="$(wc -c <"${DIR}/db.dump" | tr -d ' ')"
hs_log "wrote db.dump (${db_bytes} bytes)"

object_backend=""
object_note="not configured"
if [ -f "${ENV_FILE}" ]; then
	object_backend="$(sed -n 's/^HOOKSHIP_PAYLOAD_BACKEND=//p' "${ENV_FILE}" | tail -n1)"
fi
if [ "${object_backend}" = "object-store" ]; then
	if command -v mc >/dev/null 2>&1; then
		endpoint="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_ENDPOINT=//p' "${ENV_FILE}" | tail -n1)"
		bucket="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_BUCKET=//p' "${ENV_FILE}" | tail -n1)"
		access="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_ACCESS_KEY_ID=//p' "${ENV_FILE}" | tail -n1)"
		secret="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_SECRET_ACCESS_KEY=//p' "${ENV_FILE}" | tail -n1)"
		use_tls="$(sed -n 's/^HOOKSHIP_PAYLOAD_OBJECT_USE_TLS=//p' "${ENV_FILE}" | tail -n1)"
		scheme="http"; [ "${use_tls}" = "true" ] && scheme="https"
		if [ -n "${endpoint}" ] && [ -n "${bucket}" ]; then
			hs_log "mirroring object-store bucket ${bucket}"
			mkdir -p "${DIR}/objects"
			mc alias set hookship-backup "${scheme}://${endpoint}" "${access}" "${secret}" >/dev/null
			mc mirror --overwrite "hookship-backup/${bucket}" "${DIR}/objects"
			mc alias remove hookship-backup >/dev/null 2>&1 || true
			object_note="mirrored bucket ${bucket}"
		else
			hs_warn "object-store configured but endpoint/bucket missing; skipping object mirror"
			object_note="skipped (incomplete object-store config)"
		fi
	else
		hs_warn "object-store payload backend configured but 'mc' is not installed; skipping object mirror"
		object_note="skipped (mc not installed)"
	fi
fi

# Portable checksum manifest (Linux sha256sum or macOS shasum), written via a
# temp file outside the backup dir to avoid reading and writing it at once.
manifest_tmp="$(mktemp)"
( cd "${DIR}" && find . -type f ! -name SHA256SUMS | sort | while read -r f; do
	hs_sha256 "${f}"
done ) >"${manifest_tmp}"
mv "${manifest_tmp}" "${DIR}/SHA256SUMS"

db_sha="$(hs_sha256 "${DIR}/db.dump" | awk '{print $1}')"
cat >"${DIR}/manifest.json" <<EOF
{
  "app": "hook-service",
  "timestamp": "${STAMP}",
  "createdAt": "$(date -u +%Y-%m-%dT%H:%M:%SZ)",
  "database": {
    "dump": "db.dump",
    "format": "pg_dump-custom",
    "bytes": ${db_bytes},
    "sha256": "${db_sha}",
    "dsn": "$(hs_redact_dsn "${DSN}")"
  },
  "objectStore": "${object_note}"
}
EOF

hs_log "backup complete: ${DIR}"
hs_log "verify later with: (cd '${DIR}' && sha256sum -c SHA256SUMS)"
printf '%s\n' "${DIR}"
