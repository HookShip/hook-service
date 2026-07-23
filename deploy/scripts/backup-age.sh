#!/usr/bin/env bash
#
# backup-age.sh — report the age of the most recent backup and fail if it is
# older than a threshold. Intended for a cron/systemd-timer health check that
# produces backup-age evidence.
#
# Usage:
#   deploy/scripts/backup-age.sh [--dir DIR] [--max-age-hours N]
#
#   --dir DIR         Parent directory holding hookship-* backups (default
#                     /var/lib/hookship/backups, falling back to ./backups).
#   --max-age-hours N Fail if the newest backup is older than N hours (default 26).
#
# Exit codes: 0 fresh, 1 stale, 2 no backups found or usage error.
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

DIR=""
MAX_AGE_HOURS=26

while [ "$#" -gt 0 ]; do
	case "$1" in
	--dir) DIR="${2:?--dir requires a value}"; shift 2 ;;
	--max-age-hours) MAX_AGE_HOURS="${2:?--max-age-hours requires a value}"; shift 2 ;;
	-h | --help) sed -n '2,16p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

if [ -z "${DIR}" ]; then
	if [ -d /var/lib/hookship/backups ]; then DIR="/var/lib/hookship/backups"; else DIR="./backups"; fi
fi
[ -d "${DIR}" ] || { printf 'error: backup directory not found: %s\n' "${DIR}" >&2; exit 2; }

latest="$(find "${DIR}" -maxdepth 1 -type d -name 'hookship-*' | sort | tail -n1)"
[ -n "${latest}" ] || { printf 'error: no hookship-* backups in %s\n' "${DIR}" >&2; exit 2; }

# Prefer the manifest timestamp; fall back to the directory mtime.
now="$(date -u +%s)"
mtime="$(date -u -r "${latest}" +%s 2>/dev/null || stat -c %Y "${latest}" 2>/dev/null || echo "${now}")"
age_seconds=$((now - mtime))
age_hours=$((age_seconds / 3600))

printf 'latest backup: %s\n' "${latest}"
printf 'age: %dh (threshold %dh)\n' "${age_hours}" "${MAX_AGE_HOURS}"

if [ "${age_hours}" -gt "${MAX_AGE_HOURS}" ]; then
	printf 'STALE: newest backup exceeds the freshness threshold\n' >&2
	exit 1
fi
printf 'OK: backup is fresh\n'
