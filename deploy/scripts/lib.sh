#!/usr/bin/env bash
# Shared helpers for the hook-service operational scripts. Source this file:
#   # shellcheck source=deploy/scripts/lib.sh
#   . "$(dirname -- "$0")/lib.sh"
#
# It provides logging, tool checks, DSN resolution/redaction, and a portable
# sha256 helper (Linux sha256sum or macOS shasum). It reads no secret from the
# environment beyond what the caller passes in.

hs_log()  { printf '==> %s\n' "$*"; }
hs_warn() { printf 'warning: %s\n' "$*" >&2; }
hs_die()  { printf 'error: %s\n' "$*" >&2; exit 1; }

# hs_require TOOL... — fail unless every named command exists.
hs_require() {
	local tool
	for tool in "$@"; do
		command -v "${tool}" >/dev/null 2>&1 || hs_die "required tool not found: ${tool}"
	done
}

# hs_resolve_dsn [explicit] — resolve the PostgreSQL DSN from, in order: the
# explicit argument, HOOKSHIP_DATABASE_URL, or the deployment env file
# (HOOKSHIP_ENV_FILE, default /etc/hookship/hookshipd.env). Prints empty if none.
hs_resolve_dsn() {
	local explicit="${1:-}"
	if [ -n "${explicit}" ]; then printf '%s' "${explicit}"; return 0; fi
	if [ -n "${HOOKSHIP_DATABASE_URL:-}" ]; then printf '%s' "${HOOKSHIP_DATABASE_URL}"; return 0; fi
	local envfile="${HOOKSHIP_ENV_FILE:-/etc/hookship/hookshipd.env}"
	if [ -f "${envfile}" ]; then
		sed -n 's/^HOOKSHIP_DATABASE_URL=//p' "${envfile}" | tail -n1
		return 0
	fi
	printf ''
}

# hs_redact_dsn DSN — hide the password in a connection URL for logging.
hs_redact_dsn() {
	printf '%s' "$1" | sed -E 's#(://[^:/@]+):[^@]*@#\1:REDACTED@#'
}

# hs_sha256 FILE... — portable SHA-256 (sha256sum or shasum -a 256).
hs_sha256() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum "$@"
	else
		shasum -a 256 "$@"
	fi
}

# hs_sha256_check FILE — verify a SHA256SUMS manifest in its own directory.
hs_sha256_check() {
	if command -v sha256sum >/dev/null 2>&1; then
		sha256sum -c "$1"
	else
		shasum -a 256 -c "$1"
	fi
}

# hs_timestamp — UTC timestamp safe for filenames, e.g. 20260721T130405Z.
hs_timestamp() { date -u +%Y%m%dT%H%M%SZ; }
