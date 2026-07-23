#!/usr/bin/env bash
#
# run.sh — hermetic test runner for the operational shell scripts.
#
# Every *_test.sh in this directory is executed in its own process. Tests are
# hermetic: they use pure helpers from lib.sh and, where a script shells out to
# docker, a fake `docker` shim on PATH. No real containers, database, or network
# are required, so this runs in the ordinary `make check` lane.
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
pass=0
fail=0

for test_file in "${TEST_DIR}"/*_test.sh; do
	[ -e "${test_file}" ] || continue
	name="$(basename "${test_file}")"
	if bash "${test_file}"; then
		printf 'ok   %s\n' "${name}"
		pass=$((pass + 1))
	else
		printf 'FAIL %s\n' "${name}"
		fail=$((fail + 1))
	fi
done

printf '\n%d passed, %d failed\n' "${pass}" "${fail}"
[ "${fail}" -eq 0 ]
