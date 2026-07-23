#!/usr/bin/env bash
#
# rollback_test.sh — hermetic tests for rollback.sh --mode pull --check-only.
#
# A fake `docker` on PATH lets us assert two audit-critical properties without a
# real Docker daemon or database:
#   1. rollback.sh probes using the ACTUAL derived Compose network, not a
#      hard-coded name.
#   2. it only reports "schema incompatible" when the migrate probe genuinely
#      fails closed, and reports a distinct "cannot probe" error otherwise.
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
SCRIPTS_DIR="$(cd -- "${TEST_DIR}/.." && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"

ROLLBACK="${SCRIPTS_DIR}/rollback.sh"
IMAGE="ghcr.io/example/hook-service@sha256:$(printf '0%.0s' $(seq 1 64))"

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

# Minimal fake Compose project directory + env file (contents are irrelevant to
# the fake docker but the files must exist).
mkdir -p "${WORK}/compose"
: >"${WORK}/compose/compose.pull.yaml"
printf 'HOOKSHIP_DATABASE_URL=postgres://hookship:x@postgres:5432/hookship\n' >"${WORK}/compose/.env"

# Put the fake docker first on PATH.
export PATH="${TEST_DIR}/fakebin:${PATH}"
export HS_COMPOSE_DIR="${WORK}/compose"
export HS_COMPOSE_ENV_FILE="${WORK}/compose/.env"

run_rollback() {
	HS_FAKE_RUN_LOG="${WORK}/run.log" \
		bash "${ROLLBACK}" --mode pull --image "${IMAGE}" --check-only 2>&1
}

# --- Case 1: derived network is used (default project) ---
: >"${WORK}/run.log"
rc=0
out="$(HS_FAKE_MIGRATE=ok HS_FAKE_NETWORK=hookship-deploy_default run_rollback)" || rc=$?
assert_status 0 "${rc}" "compatible probe exits 0"
assert_contains "$(cat "${WORK}/run.log")" "--network hookship-deploy_default" "uses derived default network"
assert_contains "${out}" "schema-compatible" "reports compatible"

# --- Case 2: a RENAMED project network is derived, not hard-coded ---
: >"${WORK}/run.log"
rc=0
out="$(HS_FAKE_MIGRATE=ok HS_FAKE_NETWORK=renamed-proj_default run_rollback)" || rc=$?
assert_status 0 "${rc}" "renamed-project probe exits 0"
assert_contains "$(cat "${WORK}/run.log")" "--network renamed-proj_default" "derives renamed network"
assert_not_contains "$(cat "${WORK}/run.log")" "hookship-deploy_default" "no hard-coded network"

# --- Case 3: genuine schema incompatibility is reported as such ---
rc=0
out="$(HS_FAKE_MIGRATE=incompat HS_FAKE_NETWORK=hookship-deploy_default run_rollback)" || rc=$?
assert_status 1 "${rc}" "incompatible probe exits 1"
assert_contains "${out}" "NOT schema-compatible" "reports incompatible"
assert_contains "${out}" "reverts both the code and the schema" "points at restore"

# --- Case 4: an infra failure is NOT reported as incompatible ---
rc=0
out="$(HS_FAKE_MIGRATE=error HS_FAKE_NETWORK=hookship-deploy_default run_rollback)" || rc=$?
assert_status 1 "${rc}" "infra failure exits non-zero"
assert_contains "${out}" "could not run the schema compatibility probe" "distinct infra error"
assert_not_contains "${out}" "NOT schema-compatible" "does not claim incompatible on infra failure"

# --- Case 5: stack down is reported clearly, not as incompatible ---
rc=0
out="$(HS_FAKE_PS_EMPTY=1 HS_FAKE_MIGRATE=ok run_rollback)" || rc=$?
assert_status 1 "${rc}" "stack down exits non-zero"
assert_contains "${out}" "Compose stack is not running" "reports stack down"

assert_done
