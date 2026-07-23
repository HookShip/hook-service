#!/usr/bin/env bash
#
# assert.sh — tiny assertion helpers shared by the hermetic shell tests.
set -euo pipefail

ASSERT_FAILURES=0

assert_eq() {
	local expected="$1" actual="$2" msg="${3:-}"
	if [ "${expected}" != "${actual}" ]; then
		printf 'assertion failed%s\n  expected: %s\n  actual:   %s\n' \
			"${msg:+ (${msg})}" "${expected}" "${actual}" >&2
		ASSERT_FAILURES=$((ASSERT_FAILURES + 1))
	fi
}

assert_status() {
	local expected="$1" actual="$2" msg="${3:-}"
	if [ "${expected}" != "${actual}" ]; then
		printf 'status assertion failed%s\n  expected exit: %s\n  actual exit:   %s\n' \
			"${msg:+ (${msg})}" "${expected}" "${actual}" >&2
		ASSERT_FAILURES=$((ASSERT_FAILURES + 1))
	fi
}

assert_contains() {
	local haystack="$1" needle="$2" msg="${3:-}"
	case "${haystack}" in
	*"${needle}"*) ;;
	*)
		printf 'contains assertion failed%s\n  needle:   %s\n  haystack: %s\n' \
			"${msg:+ (${msg})}" "${needle}" "${haystack}" >&2
		ASSERT_FAILURES=$((ASSERT_FAILURES + 1))
		;;
	esac
}

assert_not_contains() {
	local haystack="$1" needle="$2" msg="${3:-}"
	case "${haystack}" in
	*"${needle}"*)
		printf 'not-contains assertion failed%s\n  needle:   %s\n  haystack: %s\n' \
			"${msg:+ (${msg})}" "${needle}" "${haystack}" >&2
		ASSERT_FAILURES=$((ASSERT_FAILURES + 1))
		;;
	esac
}

assert_done() {
	[ "${ASSERT_FAILURES}" -eq 0 ]
}
