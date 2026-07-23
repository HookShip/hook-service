#!/usr/bin/env bash
#
# rollback.sh — schema-compatible rollback of a single-VM deployment.
#
# hook-service migrations are forward-only, so a rollback redeploys the PREVIOUS
# code WITHOUT reverting the schema. That is safe only if the newer release did
# not add a migration the older code lacks. This script probes that precisely:
# it deploys the previous binaries/image and runs `hookshipctl migrate`, which
# fails closed if the database contains a migration the code does not know. On
# such a failure it refuses to start the older version and tells you to restore
# from the pre-upgrade backup instead (which reverts both code and schema).
#
# Usage:
#   sudo deploy/scripts/rollback.sh --mode systemd [--to-bindir DIR] [--dsn DSN]
#        deploy/scripts/rollback.sh --mode pull --image PREV@sha256:...
#
# Default --to-bindir is /var/lib/hookship/rollback (populated by upgrade.sh).
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

MODE="systemd"
TO_BINDIR="/var/lib/hookship/rollback"
IMAGE=""
DSN_ARG=""
BIN_INSTALL_DIR="/usr/local/bin"

while [ "$#" -gt 0 ]; do
	case "$1" in
	--mode) MODE="${2:?}"; shift 2 ;;
	--to-bindir) TO_BINDIR="${2:?}"; shift 2 ;;
	--image) IMAGE="${2:?}"; shift 2 ;;
	--dsn) DSN_ARG="${2:?}"; shift 2 ;;
	-h | --help) sed -n '2,20p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done

wait_ready() {
	local url="$1" attempt=0
	until curl -fsSk "${url}" >/dev/null 2>&1; do
		attempt=$((attempt + 1))
		[ "${attempt}" -gt 60 ] && return 1
		sleep 1
	done
	return 0
}

incompatible_message() {
	cat >&2 <<'EOF'
error: the previous version is NOT schema-compatible with the current database.

The upgrade applied a forward-only migration the older code does not know, so a
binary/image rollback alone would fail readiness. Restore from the pre-upgrade
backup instead, which reverts both the code and the schema:

  # stop the service, then:
  sudo deploy/scripts/restore.sh --input /var/lib/hookship/backups/hookship-<stamp> --dsn <DSN>
  # redeploy the previous binaries/image, then start the service.
EOF
	exit 1
}

case "${MODE}" in
systemd)
	[ "$(id -u)" -eq 0 ] || hs_die "systemd rollback must run as root"
	[ -x "${TO_BINDIR}/hookshipd" ] && [ -x "${TO_BINDIR}/hookshipctl" ] ||
		hs_die "previous binaries not found in ${TO_BINDIR} (was this deployment upgraded with upgrade.sh?)"
	hs_require systemctl curl

	hs_log "installing previous binaries from ${TO_BINDIR}"
	install -o root -g root -m 0755 "${TO_BINDIR}/hookshipd" "${BIN_INSTALL_DIR}/hookshipd"
	install -o root -g root -m 0755 "${TO_BINDIR}/hookshipctl" "${BIN_INSTALL_DIR}/hookshipctl"

	hs_log "probing schema compatibility with the previous code"
	DSN="$(hs_resolve_dsn "${DSN_ARG}")"
	[ -n "${DSN}" ] || hs_die "no DSN to probe; pass --dsn or set the env file"
	if ! HOOKSHIP_BACKEND=postgres HOOKSHIP_DATABASE_URL="${DSN}" "${BIN_INSTALL_DIR}/hookshipctl" migrate >/dev/null 2>&1; then
		incompatible_message
	fi

	hs_log "schema-compatible; restarting hookshipd on the previous version"
	systemctl restart hookshipd.service
	if wait_ready "http://127.0.0.1:8080/readyz"; then
		hs_log "rollback complete and ready"
	else
		systemctl status --no-pager hookshipd.service || true
		hs_die "previous version did not become ready; restore from backup"
	fi
	;;
pull)
	[ -n "${IMAGE}" ] || hs_die "--image PREV@sha256:... (the previous immutable digest) is required"
	case "${IMAGE}" in *@sha256:*) ;; *) hs_die "--image must be an immutable digest (…@sha256:…)" ;; esac
	env_file="${SCRIPT_DIR}/../compose/.env"
	[ -f "${env_file}" ] || hs_die "compose .env not found"
	hs_log "probing schema compatibility of ${IMAGE} against the running database"
	# The previous image's migrate fails closed if the DB is ahead of its code.
	if ! docker run --rm --env-file "${env_file}" -e HOOKSHIP_BACKEND=postgres \
		--network "hookship-deploy_default" --entrypoint /hookshipctl "${IMAGE}" migrate >/dev/null 2>&1; then
		incompatible_message
	fi
	hs_log "pinning previous image and restarting"
	if grep -q '^HOOKSHIP_IMAGE=' "${env_file}"; then
		sed -i.bak "s#^HOOKSHIP_IMAGE=.*#HOOKSHIP_IMAGE=${IMAGE}#" "${env_file}" && rm -f "${env_file}.bak"
	else
		printf 'HOOKSHIP_IMAGE=%s\n' "${IMAGE}" >>"${env_file}"
	fi
	"${SCRIPT_DIR}/compose-up.sh" pull
	;;
*) hs_die "unknown --mode ${MODE} (expected systemd|pull)" ;;
esac
