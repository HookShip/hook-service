#!/usr/bin/env bash
#
# upgrade.sh — upgrade a single-VM hook-service deployment safely.
#
# Takes a backup first, applies migrations, and restarts, waiting for readiness.
# For the systemd path it snapshots the current binaries so rollback.sh can
# redeploy them. Migrations are forward-only; if an upgrade cannot be rolled
# back by binary alone, rollback.sh will say so and point at a restore.
#
# Usage:
#   sudo deploy/scripts/upgrade.sh --mode systemd --bindir DIR [--dsn DSN] [--no-backup]
#        deploy/scripts/upgrade.sh --mode build   [--no-backup]
#        deploy/scripts/upgrade.sh --mode pull --image REF@sha256:... [--no-backup]
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

MODE="systemd"
BINDIR=""
IMAGE=""
DSN_ARG=""
DO_BACKUP="yes"
ROLLBACK_DIR="/var/lib/hookship/rollback"
BIN_INSTALL_DIR="/usr/local/bin"

while [ "$#" -gt 0 ]; do
	case "$1" in
	--mode) MODE="${2:?}"; shift 2 ;;
	--bindir) BINDIR="${2:?}"; shift 2 ;;
	--image) IMAGE="${2:?}"; shift 2 ;;
	--dsn) DSN_ARG="${2:?}"; shift 2 ;;
	--no-backup) DO_BACKUP="no"; shift ;;
	-h | --help) sed -n '2,18p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

wait_ready() {
	local url="$1" attempt=0
	hs_log "waiting for readiness at ${url}"
	until curl -fsSk "${url}" >/dev/null 2>&1; do
		attempt=$((attempt + 1))
		[ "${attempt}" -gt 60 ] && return 1
		sleep 1
	done
	return 0
}

backup_first() {
	[ "${DO_BACKUP}" = "yes" ] || { hs_warn "skipping backup (--no-backup)"; return; }
	hs_log "taking a pre-upgrade backup"
	"${SCRIPT_DIR}/backup.sh" ${DSN_ARG:+--dsn "${DSN_ARG}"} >/dev/null
}

case "${MODE}" in
systemd)
	[ "$(id -u)" -eq 0 ] || hs_die "systemd upgrade must run as root"
	[ -n "${BINDIR}" ] || hs_die "--bindir DIR with the new binaries is required"
	[ -x "${BINDIR}/hookshipd" ] && [ -x "${BINDIR}/hookshipctl" ] || hs_die "new binaries not found in ${BINDIR}"
	hs_require systemctl curl
	backup_first

	hs_log "snapshotting current binaries to ${ROLLBACK_DIR}"
	install -d -m 0750 "${ROLLBACK_DIR}"
	for bin in hookshipd hookshipctl; do
		[ -x "${BIN_INSTALL_DIR}/${bin}" ] && cp -f "${BIN_INSTALL_DIR}/${bin}" "${ROLLBACK_DIR}/${bin}" || true
	done

	hs_log "installing new binaries"
	install -o root -g root -m 0755 "${BINDIR}/hookshipd" "${BIN_INSTALL_DIR}/hookshipd"
	install -o root -g root -m 0755 "${BINDIR}/hookshipctl" "${BIN_INSTALL_DIR}/hookshipctl"

	hs_log "applying migrations"
	systemctl restart hookship-migrate.service
	hs_log "restarting hookshipd"
	systemctl restart hookshipd.service

	if wait_ready "http://127.0.0.1:8080/readyz"; then
		hs_log "upgrade complete and ready"
	else
		systemctl status --no-pager hookshipd.service || true
		hs_die "service did not become ready; roll back with: sudo deploy/scripts/rollback.sh --mode systemd"
	fi
	;;
build)
	backup_first
	hs_log "rebuilding and restarting the source-build stack"
	"${SCRIPT_DIR}/compose-up.sh" build
	;;
pull)
	[ -n "${IMAGE}" ] || hs_die "--image REF@sha256:... is required for the pull path"
	case "${IMAGE}" in *@sha256:*) ;; *) hs_die "--image must be an immutable digest (…@sha256:…)" ;; esac
	backup_first
	env_file="${SCRIPT_DIR}/../compose/.env"
	[ -f "${env_file}" ] || hs_die "compose .env not found; deploy first with compose-up.sh pull"
	hs_log "pinning HOOKSHIP_IMAGE=${IMAGE}"
	if grep -q '^HOOKSHIP_IMAGE=' "${env_file}"; then
		sed -i.bak "s#^HOOKSHIP_IMAGE=.*#HOOKSHIP_IMAGE=${IMAGE}#" "${env_file}" && rm -f "${env_file}.bak"
	else
		printf 'HOOKSHIP_IMAGE=%s\n' "${IMAGE}" >>"${env_file}"
	fi
	"${SCRIPT_DIR}/compose-up.sh" pull
	;;
*) hs_die "unknown --mode ${MODE} (expected systemd|build|pull)" ;;
esac
