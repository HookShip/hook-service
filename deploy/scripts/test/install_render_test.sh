#!/usr/bin/env bash
#
# install_render_test.sh — hermetic checks that install.sh renders the systemd
# unit ExecStart paths from the selected --prefix, so `--prefix` works for every
# installed unit (including the backup service). It sources install.sh for its
# pure render_unit_to helper without executing the installer, so no root,
# systemd, or filesystem writes are required.
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"

DEPLOY_SCRIPTS="${TEST_DIR}/.."
SYSTEMD_DIR="${DEPLOY_SCRIPTS}/../systemd"

# Load the installer's helpers without running main (the source guard checks
# this variable). "$@" is empty here, so its argument parser is a no-op.
export HOOKSHIP_INSTALL_SOURCE_ONLY=1
# shellcheck source=deploy/scripts/install.sh
. "${DEPLOY_SCRIPTS}/install.sh"

# --- default prefix: ExecStart paths are unchanged -------------------------

PREFIX="/usr/local"; BIN_INSTALL_DIR="${PREFIX}/bin"; LIB_DIR="${PREFIX}/lib/hookship"
out="$(render_unit_to "${SYSTEMD_DIR}/hookshipd.service")"
assert_contains "${out}" "ExecStart=/usr/local/bin/hookshipd all" "default prefix keeps hookshipd path"
out="$(render_unit_to "${SYSTEMD_DIR}/hookship-backup.service")"
assert_contains "${out}" "ExecStart=/usr/local/lib/hookship/backup.sh" "default prefix keeps backup path"

# --- custom prefix: every unit's ExecStart is rewritten --------------------

PREFIX="/opt/hookship"; BIN_INSTALL_DIR="${PREFIX}/bin"; LIB_DIR="${PREFIX}/lib/hookship"

out="$(render_unit_to "${SYSTEMD_DIR}/hookshipd.service")"
assert_contains "${out}" "ExecStart=/opt/hookship/bin/hookshipd all" "custom prefix rewrites hookshipd"
assert_not_contains "${out}" "/usr/local/bin/hookshipd" "no stale default bin path remains"

out="$(render_unit_to "${SYSTEMD_DIR}/hookship-migrate.service")"
assert_contains "${out}" "ExecStart=/opt/hookship/bin/hookshipctl migrate" "custom prefix rewrites migrate"

out="$(render_unit_to "${SYSTEMD_DIR}/hookship-backup.service")"
assert_contains "${out}" "ExecStart=/opt/hookship/lib/hookship/backup.sh --output /var/lib/hookship/backups" "custom prefix rewrites backup script"
assert_not_contains "${out}" "/usr/local/lib/hookship" "no stale default lib path remains"

# Prefix-independent state/config paths must not be rewritten.
assert_contains "${out}" "/var/lib/hookship/backups" "state dir path is untouched by prefix"

# The timer has no ExecStart and must pass through unchanged.
out="$(render_unit_to "${SYSTEMD_DIR}/hookship-backup.timer")"
assert_contains "${out}" "Unit=hookship-backup.service" "timer renders through unchanged"

assert_done
