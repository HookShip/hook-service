#!/usr/bin/env bash
#
# uninstall.sh — safely remove the hook-service systemd deployment.
#
# By default this performs a SAFE teardown: it stops and disables the services
# and removes the binaries and unit files, but PRESERVES the configuration
# (/etc/hookship), the runtime state (/var/lib/hookship), the service user, and
# the external PostgreSQL database. Pass --purge to also remove the config,
# state, and service account. The database is never touched by this script.
#
# Usage:
#   sudo deploy/scripts/uninstall.sh [--purge] [--prefix DIR] [--yes]
#
#   --purge        Also remove ${ETC_DIR}, ${STATE_DIR}, and the service account.
#   --prefix DIR   Binary install prefix used at install time (default /usr/local).
#   --yes          Do not prompt for confirmation on --purge.
set -euo pipefail

SERVICE_USER="hookship"
SERVICE_GROUP="hookship"
ETC_DIR="/etc/hookship"
STATE_DIR="/var/lib/hookship"
UNIT_DIR="/etc/systemd/system"
PREFIX="/usr/local"
PURGE="no"
ASSUME_YES="no"

log()  { printf '==> %s\n' "$*"; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

while [ "$#" -gt 0 ]; do
	case "$1" in
	--purge) PURGE="yes"; shift ;;
	--prefix) PREFIX="${2:?--prefix requires a value}"; shift 2 ;;
	--yes) ASSUME_YES="yes"; shift ;;
	-h | --help) sed -n '2,20p' "$0"; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

BIN_INSTALL_DIR="${PREFIX}/bin"

[ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"
command -v systemctl >/dev/null 2>&1 || die "systemctl not found; systemd is required"

stop_units() {
	local unit
	for unit in hookship-backup.timer hookship-backup.service hookshipd.service hookship-migrate.service; do
		if systemctl list-unit-files "${unit}" >/dev/null 2>&1 && systemctl cat "${unit}" >/dev/null 2>&1; then
			log "stopping and disabling ${unit}"
			systemctl disable --now "${unit}" >/dev/null 2>&1 || true
		fi
	done
}

remove_units() {
	local removed="no"
	local unit
	for unit in hookship-backup.timer hookship-backup.service hookshipd.service hookship-migrate.service; do
		if [ -e "${UNIT_DIR}/${unit}" ]; then
			log "removing ${UNIT_DIR}/${unit}"
			rm -f "${UNIT_DIR}/${unit}"
			removed="yes"
		fi
	done
	if [ "${removed}" = "yes" ]; then
		systemctl daemon-reload || true
	fi
}

remove_operational_scripts() {
	local lib_dir="${PREFIX}/lib/hookship"
	if [ -d "${lib_dir}" ]; then
		log "removing operational scripts in ${lib_dir}"
		rm -f "${lib_dir}/lib.sh" "${lib_dir}/backup.sh" "${lib_dir}/backup-age.sh"
		rmdir "${lib_dir}" 2>/dev/null || true
	fi
}

remove_binaries() {
	local bin
	for bin in hookshipd hookshipctl; do
		if [ -e "${BIN_INSTALL_DIR}/${bin}" ]; then
			log "removing ${BIN_INSTALL_DIR}/${bin}"
			rm -f "${BIN_INSTALL_DIR}/${bin}"
		fi
	done
}

purge_state() {
	if [ "${ASSUME_YES}" != "yes" ]; then
		printf 'Remove config (%s), state (%s), and the %s user? [y/N] ' \
			"${ETC_DIR}" "${STATE_DIR}" "${SERVICE_USER}"
		read -r reply
		case "${reply}" in
		y | Y | yes | YES) ;;
		*) die "aborted; nothing purged" ;;
		esac
	fi
	log "removing ${ETC_DIR} and ${STATE_DIR}"
	rm -rf "${ETC_DIR}" "${STATE_DIR}"
	if getent passwd "${SERVICE_USER}" >/dev/null 2>&1; then
		log "removing user ${SERVICE_USER}"
		userdel "${SERVICE_USER}" >/dev/null 2>&1 || true
	fi
	if getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
		log "removing group ${SERVICE_GROUP}"
		groupdel "${SERVICE_GROUP}" >/dev/null 2>&1 || true
	fi
	log "purge complete (the external PostgreSQL database was not touched)"
}

main() {
	stop_units
	remove_units
	remove_binaries
	remove_operational_scripts
	if [ "${PURGE}" = "yes" ]; then
		purge_state
	else
		log "safe teardown complete"
		log "preserved: ${ETC_DIR}, ${STATE_DIR}, the ${SERVICE_USER} user, and the database"
		log "run with --purge to remove config, state, and the service account"
	fi
}

main "$@"
