#!/usr/bin/env bash
#
# compose-up.sh — run the single-VM Compose deployment (source-build or pull).
#
# Usage:
#   deploy/scripts/compose-up.sh build [extra docker compose args...]
#   deploy/scripts/compose-up.sh pull  [extra docker compose args...]
#
# With no extra args it validates prerequisites, brings the stack up detached,
# and waits for readiness. With extra args it passes them straight to
# `docker compose` for the chosen path, e.g.:
#   deploy/scripts/compose-up.sh build down -v
#   deploy/scripts/compose-up.sh pull logs -f hookshipd
#
# The pull path REQUIRES HOOKSHIP_IMAGE to be an immutable digest reference
# (…@sha256:<64-hex>); a mutable tag is rejected. Neither path publishes
# anything or invents a registry.
set -euo pipefail

log() { printf '==> %s\n' "$*"; }
die() { printf 'error: %s\n' "$*" >&2; exit 1; }

MODE="${1:-build}"
shift || true
case "${MODE}" in
build) COMPOSE_FILE="compose.build.yaml" ;;
pull) COMPOSE_FILE="compose.pull.yaml" ;;
-h | --help) sed -n '2,20p' "$0"; exit 0 ;;
*) die "mode must be 'build' or 'pull', got '${MODE}'" ;;
esac

COMPOSE_DIR="$(cd -- "$(dirname -- "$0")/../compose" && pwd)"
cd "${COMPOSE_DIR}"

command -v docker >/dev/null 2>&1 || die "docker is required"
docker compose version >/dev/null 2>&1 || die "docker compose v2 is required"

compose() { docker compose -f "${COMPOSE_FILE}" "$@"; }

# Passthrough mode: hand extra args straight to docker compose.
if [ "$#" -gt 0 ]; then
	exec docker compose -f "${COMPOSE_FILE}" "$@"
fi

[ -f .env ] || {
	cp .env.example .env
	die "created .env from .env.example — edit it (replace every REPLACE_ME), then re-run"
}
[ -f assignments.yaml ] || {
	cp ../assignments.example.yaml assignments.yaml
	die "created assignments.yaml from the example — edit it, then re-run"
}

if grep -q "REPLACE_ME" .env assignments.yaml; then
	die "configuration is incomplete: replace every REPLACE_ME in .env and assignments.yaml"
fi

if [ "${MODE}" = "pull" ]; then
	# shellcheck disable=SC1091
	image="$(sed -n 's/^HOOKSHIP_IMAGE=//p' .env | tail -n1)"
	[ -n "${image}" ] || die "HOOKSHIP_IMAGE is required for the pull path"
	case "${image}" in
	*@sha256:*)
		digest="${image##*@sha256:}"
		printf '%s' "${digest}" | grep -Eq '^[0-9a-f]{64}$' ||
			die "HOOKSHIP_IMAGE digest must be 64 lowercase hex chars: ${image}"
		;;
	*) die "pull mode requires an immutable digest (…@sha256:…), got: ${image}" ;;
	esac
	log "using immutable image ${image}"
fi

if [ "${MODE}" = "build" ]; then
	log "building and starting the source-build stack"
	compose up -d --build
else
	log "starting the pull stack"
	compose up -d
fi

log "waiting for readiness at http://127.0.0.1:8080/readyz"
attempt=0
until curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [ "${attempt}" -gt 90 ]; then
		compose ps
		die "hookshipd did not become ready"
	fi
	sleep 1
done
log "hookshipd is ready:"
curl -fsS http://127.0.0.1:8080/readyz || true
printf '\n'
log "tear down with: deploy/scripts/compose-up.sh ${MODE} down -v"
