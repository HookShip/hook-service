#!/usr/bin/env bash
#
# coverage.sh — deterministic coverage gate. It measures unit coverage for the
# critical packages and, when a database (and optionally the object store and
# brokers) is configured, integration coverage for the durability and backend
# packages. Each measurement is compared against a documented floor; the script
# fails if any package regresses below its floor.
#
# Usage:
#   scripts/coverage.sh                 # unit floors only
#   TEST_DATABASE_URL=... scripts/coverage.sh   # unit + integration floors
#
# The floors are a ratchet: raise them when coverage rises, never lower a floor
# to make a regression pass. See docs/testing.md.
set -euo pipefail

GO="${GO:-go}"
fail=0

# check LANE PKG FLOOR [extra go-test flags...]
check() {
	local lane="$1" pkg="$2" floor="$3"
	shift 3
	local cov
	cov="$("${GO}" test -count=1 "$@" -cover "${pkg}" 2>/dev/null |
		sed -nE 's/.*coverage: ([0-9.]+)% of statements.*/\1/p' | head -n1)"
	if [ -z "${cov}" ]; then
		printf 'FAIL  %-6s %-42s no coverage measured\n' "${lane}" "${pkg#./}"
		fail=1
		return
	fi
	if awk "BEGIN { exit !(${cov} >= ${floor}) }"; then
		printf 'ok    %-6s %-42s %6s%% (floor %s%%)\n' "${lane}" "${pkg#./}" "${cov}" "${floor}"
	else
		printf 'FAIL  %-6s %-42s %6s%% < floor %s%%\n' "${lane}" "${pkg#./}" "${cov}" "${floor}"
		fail=1
	fi
}

echo "== unit coverage floors =="
check unit ./internal/idempotency 99
check unit ./internal/capabilities 97
check unit ./internal/signing 88
check unit ./internal/ratelimit 88
check unit ./internal/worker 82
check unit ./internal/transport 81
check unit ./internal/runtime 75
check unit ./internal/config 73
check unit ./internal/ingest 72
# The durability and backend packages are database wrappers. Their pure decision
# logic is unit-tested (roughly doubling their unit coverage from ~10%), but the
# SQL orchestration is only measurable against a real database, so these two
# LOW unit floors are deliberately complemented by the MANDATORY integration
# floors below (56%/73%), enforced in CI's PostgreSQL/MinIO job. A regression in
# the SQL paths is caught by the integration floors, not the unit floors.
check unit ./internal/backend/postgres 19
check unit ./internal/durability/postgres 18

# The durability and backend packages are database wrappers: their pure decision
# logic is covered above, and the SQL orchestration is only measurable with a
# real database. When one is configured, enforce the integration floors too.
if [ -n "${TEST_DATABASE_URL:-}" ]; then
	echo "== integration coverage floors (TEST_DATABASE_URL set) =="
	check integ ./internal/durability/postgres 56 -tags=integration
	check integ ./internal/backend/postgres 73 -tags=integration
else
	echo "== integration coverage floors skipped (set TEST_DATABASE_URL to enforce) =="
fi

if [ "${fail}" -ne 0 ]; then
	echo "coverage gate FAILED" >&2
	exit 1
fi
echo "coverage gate passed"
