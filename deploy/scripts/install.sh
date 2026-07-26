#!/usr/bin/env bash
#
# install.sh — install hook-service as a hardened systemd service on one Linux VM.
#
# It validates prerequisites, creates a non-root service user and directories,
# installs mode-0600 config, the binaries, and the systemd units, and (when the
# configuration is complete) applies migrations, starts the service, and waits
# for readiness. It is idempotent and safe to re-run.
#
# Usage:
#   sudo deploy/scripts/install.sh [--bindir DIR] [--prefix DIR] [--no-start] [--enable-backups]
#
#   --bindir DIR   Directory containing prebuilt hookshipd/hookshipctl binaries.
#                  Default: <repo>/bin, else build with `make build` if Go exists.
#   --prefix DIR   Install prefix for binaries (default /usr/local -> /usr/local/bin).
#   --no-start     Install everything but do not migrate/start, even if configured.
#   --enable-backups  Enable the daily hookship-backup.timer (units install regardless).
#
# This script targets a Linux host with systemd. It never publishes anything,
# never invents a registry or domain, and never writes a credential to source
# control: /etc/hookship/hookshipd.env is created from the example with mode 0600
# and must be edited in place with real values.
set -euo pipefail

SERVICE_USER="hookship"
SERVICE_GROUP="hookship"
ETC_DIR="/etc/hookship"
STATE_DIR="/var/lib/hookship"
UNIT_DIR="/etc/systemd/system"
ENV_FILE="${ETC_DIR}/hookshipd.env"
ASSIGN_FILE="${ETC_DIR}/assignments.yaml"
PREFIX="/usr/local"
BINDIR=""
START="auto"
ENABLE_BACKUPS="no"

log()  { printf '==> %s\n' "$*"; }
warn() { printf 'warning: %s\n' "$*" >&2; }
die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

DEPLOY_DIR="$(cd -- "$(dirname -- "$0")/.." && pwd)"
REPO_ROOT="$(cd -- "${DEPLOY_DIR}/.." && pwd)"

while [ "$#" -gt 0 ]; do
	case "$1" in
	--bindir) BINDIR="${2:?--bindir requires a value}"; shift 2 ;;
	--prefix) PREFIX="${2:?--prefix requires a value}"; shift 2 ;;
	--no-start) START="no"; shift ;;
	--enable-backups) ENABLE_BACKUPS="yes"; shift ;;
	-h | --help) sed -n '2,30p' "$0"; exit 0 ;;
	*) die "unknown argument: $1" ;;
	esac
done

BIN_INSTALL_DIR="${PREFIX}/bin"
LIB_DIR="${PREFIX}/lib/hookship"

require_root() {
	[ "$(id -u)" -eq 0 ] || die "must run as root (use sudo)"
}

require_linux_systemd() {
	[ "$(uname -s)" = "Linux" ] || die "this installer supports Linux with systemd only"
	command -v systemctl >/dev/null 2>&1 || die "systemctl not found; systemd is required"
	[ -d /run/systemd/system ] || warn "systemd does not appear to be the active init; unit start may fail"
}

require_tools() {
	local missing=""
	for tool in install systemctl getent curl; do
		command -v "$tool" >/dev/null 2>&1 || missing="${missing} ${tool}"
	done
	command -v useradd >/dev/null 2>&1 || command -v adduser >/dev/null 2>&1 || missing="${missing} useradd"
	[ -z "${missing}" ] || die "missing required tools:${missing}"
}

resolve_binaries() {
	if [ -n "${BINDIR}" ]; then
		if [ ! -x "${BINDIR}/hookshipd" ] || [ ! -x "${BINDIR}/hookshipctl" ]; then
			die "hookshipd/hookshipctl not found (or not executable) in ${BINDIR}"
		fi
		return
	fi
	if [ -x "${REPO_ROOT}/bin/hookshipd" ] && [ -x "${REPO_ROOT}/bin/hookshipctl" ]; then
		BINDIR="${REPO_ROOT}/bin"
		return
	fi
	if command -v go >/dev/null 2>&1; then
		log "building binaries with make build"
		make -C "${REPO_ROOT}" build >/dev/null
		BINDIR="${REPO_ROOT}/bin"
		return
	fi
	die "no prebuilt binaries found and Go is not installed; pass --bindir DIR"
}

create_service_account() {
	if getent group "${SERVICE_GROUP}" >/dev/null 2>&1; then
		log "group ${SERVICE_GROUP} already exists"
	else
		log "creating system group ${SERVICE_GROUP}"
		groupadd --system "${SERVICE_GROUP}"
	fi
	if getent passwd "${SERVICE_USER}" >/dev/null 2>&1; then
		log "user ${SERVICE_USER} already exists"
	else
		log "creating system user ${SERVICE_USER}"
		local nologin
		nologin="$(command -v nologin || echo /usr/sbin/nologin)"
		[ -x "${nologin}" ] || nologin="/bin/false"
		useradd --system --gid "${SERVICE_GROUP}" \
			--home-dir "${STATE_DIR}" --no-create-home \
			--shell "${nologin}" "${SERVICE_USER}"
	fi
}

install_directories() {
	log "creating ${ETC_DIR} and ${STATE_DIR}"
	install -d -o root -g "${SERVICE_GROUP}" -m 0750 "${ETC_DIR}"
	install -d -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" -m 0750 "${STATE_DIR}"
}

install_config() {
	install_from_example "${ENV_FILE}" 0600 hookshipd.env.example
	install_from_example "${ASSIGN_FILE}" 0640 assignments.example.yaml
}

install_from_example() {
	local dest="$1" mode="$2" src="$3"
	if [ -e "${dest}" ]; then
		log "keeping existing ${dest}"
	else
		log "installing ${dest} (mode ${mode}) from example"
		install -o "${SERVICE_USER}" -g "${SERVICE_GROUP}" -m "${mode}" "${DEPLOY_DIR}/${src}" "${dest}"
	fi
}

install_binaries() {
	log "installing binaries to ${BIN_INSTALL_DIR}"
	install -d -m 0755 "${BIN_INSTALL_DIR}"
	install -o root -g root -m 0755 "${BINDIR}/hookshipd" "${BIN_INSTALL_DIR}/hookshipd"
	install -o root -g root -m 0755 "${BINDIR}/hookshipctl" "${BIN_INSTALL_DIR}/hookshipctl"
}

# render_unit_to rewrites a systemd unit's default /usr/local ExecStart paths to
# the selected install prefix and prints it to stdout. It is pure (no I/O beyond
# reading the source) so it is unit-testable without root. When PREFIX is the
# default the substitution is a no-op, so every install renders through one path.
render_unit_to() {
	sed -e "s|/usr/local/bin|${BIN_INSTALL_DIR}|g" \
		-e "s|/usr/local/lib/hookship|${LIB_DIR}|g" "$1"
}

install_units() {
	log "installing systemd units to ${UNIT_DIR} (prefix ${PREFIX})"
	local unit tmp
	for unit in hookship-migrate.service hookshipd.service hookship-backup.service hookship-backup.timer; do
		tmp="$(mktemp)"
		render_unit_to "${DEPLOY_DIR}/systemd/${unit}" >"${tmp}"
		install -o root -g root -m 0644 "${tmp}" "${UNIT_DIR}/${unit}"
		rm -f "${tmp}"
	done
	systemctl daemon-reload
}

# install_operational_scripts installs the backup helpers (and the lib.sh they
# source) to a stable path so hookship-backup.service can run without a checkout.
install_operational_scripts() {
	local name
	log "installing operational scripts to ${LIB_DIR}"
	install -d -m 0755 "${LIB_DIR}"
	for name in lib.sh backup.sh backup-age.sh; do
		install -o root -g root -m 0755 "${DEPLOY_DIR}/scripts/${name}" "${LIB_DIR}/${name}"
	done
}

is_configured() {
	# Complete when no REPLACE_ME markers remain in the active env/config.
	! grep -q "REPLACE_ME" "${ENV_FILE}" "${ASSIGN_FILE}" 2>/dev/null
}

probe_target() {
	# Emit "scheme host:port" for the readiness probe from the env file.
	local addr scheme host port
	addr="$(sed -n 's/^HOOKSHIP_LISTEN_ADDRESS=//p' "${ENV_FILE}" | tail -n1)"
	[ -n "${addr}" ] || addr="127.0.0.1:8080"
	host="${addr%:*}"
	port="${addr##*:}"
	[ -n "${host}" ] && [ "${host}" != "0.0.0.0" ] && [ "${host}" != "::" ] || host="127.0.0.1"
	scheme="http"
	grep -Eq '^HOOKSHIP_TLS_CERT=..*' "${ENV_FILE}" && scheme="https"
	printf '%s %s:%s' "${scheme}" "${host}" "${port}"
}

start_service() {
	log "applying migrations (hookship-migrate.service)"
	systemctl enable hookship-migrate.service >/dev/null 2>&1 || true
	if ! systemctl start hookship-migrate.service; then
		systemctl status --no-pager hookship-migrate.service || true
		die "migrations failed; check the database URL in ${ENV_FILE}"
	fi

	log "enabling and starting hookshipd.service"
	systemctl enable --now hookshipd.service

	local scheme hostport url attempt curlopts
	read -r scheme hostport <<EOF
$(probe_target)
EOF
	url="${scheme}://${hostport}/readyz"
	curlopts="-fsS"
	[ "${scheme}" = "https" ] && curlopts="-fsSk"
	log "waiting for readiness at ${url}"
	attempt=0
	until curl ${curlopts} "${url}" >/dev/null 2>&1; do
		attempt=$((attempt + 1))
		if [ "${attempt}" -gt 60 ]; then
			systemctl status --no-pager hookshipd.service || true
			die "hookshipd did not become ready within 60s"
		fi
		sleep 1
	done
	log "hookshipd is ready"
	curl ${curlopts} "${url}" || true
	printf '\n'
}

main() {
	require_root
	require_linux_systemd
	require_tools
	resolve_binaries
	create_service_account
	install_directories
	install_config
	install_binaries
	install_operational_scripts
	install_units
	if [ "${ENABLE_BACKUPS}" = "yes" ]; then
		log "enabling the daily hookship-backup.timer"
		systemctl enable --now hookship-backup.timer
	fi

	if [ "${START}" = "no" ]; then
		log "installed; not starting (--no-start)"
	elif is_configured; then
		start_service
	else
		cat <<EOF

Installation complete, but configuration is incomplete.

  1. Edit ${ENV_FILE} and ${ASSIGN_FILE}, replacing every REPLACE_ME value.
  2. Then run:
       sudo systemctl enable --now hookship-migrate.service hookshipd.service

The service will not start until the configuration is complete.
EOF
	fi
	log "done"
}

# Allow the hermetic tests to source this script for its pure helpers (e.g.
# render_unit_to) without executing the installer. A normal invocation leaves
# HOOKSHIP_INSTALL_SOURCE_ONLY unset and runs main.
if [ "${HOOKSHIP_INSTALL_SOURCE_ONLY:-0}" = "0" ]; then
	main "$@"
fi
