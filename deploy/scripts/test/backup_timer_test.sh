#!/usr/bin/env bash
#
# backup_timer_test.sh — hermetic checks for the scheduled-backup wiring.
#
# It verifies backup-age.sh's freshness classification (fresh / stale / none)
# without a database, and asserts the hookship-backup systemd unit and timer
# carry the intended hardening and schedule. No real backups, containers, or
# systemd are required, so it runs in the ordinary `make test-scripts` lane.
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"

SCRIPTS_DIR="${TEST_DIR}/.."
DEPLOY_DIR="${SCRIPTS_DIR}/.."

work="$(mktemp -d)"
trap 'rm -rf "${work}"' EXIT

# --- backup-age.sh: freshness classification ------------------------------

backups="${work}/backups"
mkdir -p "${backups}/hookship-20240101T000000Z"

rc=0
bash "${SCRIPTS_DIR}/backup-age.sh" --dir "${backups}" --max-age-hours 26 >/dev/null 2>&1 || rc=$?
assert_status 0 "${rc}" "a just-created backup is fresh"

# Age the backup well past the threshold (POSIX -t works on Linux and macOS).
touch -t 200001010000 "${backups}/hookship-20240101T000000Z"
rc=0
bash "${SCRIPTS_DIR}/backup-age.sh" --dir "${backups}" --max-age-hours 26 >/dev/null 2>&1 || rc=$?
assert_status 1 "${rc}" "a year-2000 backup is stale"

mkdir -p "${work}/empty"
rc=0
bash "${SCRIPTS_DIR}/backup-age.sh" --dir "${work}/empty" --max-age-hours 26 >/dev/null 2>&1 || rc=$?
assert_status 2 "${rc}" "a directory with no backups reports absence"

# --- systemd unit + timer content -----------------------------------------

service="$(cat "${DEPLOY_DIR}/systemd/hookship-backup.service")"
timer="$(cat "${DEPLOY_DIR}/systemd/hookship-backup.timer")"

assert_contains "${service}" "Type=oneshot" "backup service is oneshot"
assert_contains "${service}" "User=hookship" "backup service runs as the service user"
assert_contains "${service}" "/usr/local/lib/hookship/backup.sh --output /var/lib/hookship/backups" "backup service invokes the installed script"
assert_contains "${service}" "ProtectSystem=strict" "backup service keeps the filesystem hardening"
assert_contains "${service}" "ReadWritePaths=/var/lib/hookship" "backup service may write only the state dir"
assert_contains "${service}" "ReadOnlyPaths=/etc/hookship" "backup service reads config read-only"
assert_contains "${service}" "CapabilityBoundingSet=" "backup service drops all capabilities"

assert_contains "${timer}" "OnCalendar=" "timer has a schedule"
assert_contains "${timer}" "Persistent=true" "timer catches up missed runs"
assert_contains "${timer}" "Unit=hookship-backup.service" "timer activates the backup service"
assert_contains "${timer}" "WantedBy=timers.target" "timer installs into timers.target"

# --- installer wiring ------------------------------------------------------

installer="$(cat "${SCRIPTS_DIR}/install.sh")"
assert_contains "${installer}" "--enable-backups" "installer exposes the opt-in flag"
assert_contains "${installer}" "hookship-backup.timer" "installer installs the timer unit"
assert_contains "${installer}" "install_operational_scripts" "installer installs the backup scripts"

# --- optional real systemd validation (only when available) ---------------

if command -v systemd-analyze >/dev/null 2>&1; then
	rc=0
	systemd-analyze verify \
		"${DEPLOY_DIR}/systemd/hookship-backup.service" \
		"${DEPLOY_DIR}/systemd/hookship-backup.timer" >/dev/null 2>&1 || rc=$?
	assert_status 0 "${rc}" "systemd-analyze verify accepts the backup units"
fi

assert_done
