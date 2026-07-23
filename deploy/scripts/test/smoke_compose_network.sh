#!/usr/bin/env bash
#
# smoke_compose_network.sh — real (non-hermetic) proof that hs_compose_network
# derives the ACTUAL Docker Compose project network rather than a hard-coded
# name. It brings up a minimal, uniquely-named disposable Compose project with a
# single Postgres service, asserts the derived network matches the real one, and
# tears everything down. Requires docker + docker compose v2.
#
# This is NOT part of `make check`; run it where docker is available:
#   deploy/scripts/test/smoke_compose_network.sh
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"
# shellcheck source=deploy/scripts/lib.sh
. "${TEST_DIR}/../lib.sh"

hs_require docker
docker compose version >/dev/null 2>&1 || hs_die "docker compose v2 is required"

PROJECT="hookship-netsmoke-$$"
WORK="$(mktemp -d)"
COMPOSE_FILE="${WORK}/compose.pull.yaml"

cleanup() {
	(cd "${WORK}" && docker compose -p "${PROJECT}" -f "${COMPOSE_FILE}" down -v >/dev/null 2>&1) || true
	rm -rf "${WORK}"
}
trap cleanup EXIT

cat >"${COMPOSE_FILE}" <<EOF
name: ${PROJECT}
services:
  postgres:
    image: postgres:17-alpine@sha256:742f40ea20b9ff2ff31db5458d127452988a2164df9e17441e191f3b72252193
    environment:
      POSTGRES_DB: hookship
      POSTGRES_USER: hookship
      POSTGRES_PASSWORD: netsmoke_only
EOF

hs_log "bringing up disposable Compose project ${PROJECT}"
(cd "${WORK}" && docker compose -f "${COMPOSE_FILE}" up -d >/dev/null)

derived="$(hs_compose_network "${WORK}" "${COMPOSE_FILE}" postgres)"
hs_log "derived network: ${derived}"

expected="$(hs_compose_default_network "${PROJECT}")"
assert_eq "${expected}" "${derived}" "derived network matches the real project default network"

# The derived network must actually exist in Docker.
if docker network inspect "${derived}" >/dev/null 2>&1; then
	hs_log "confirmed network ${derived} exists"
else
	echo "derived network ${derived} does not exist in docker" >&2
	ASSERT_FAILURES=$((ASSERT_FAILURES + 1))
fi

assert_done
