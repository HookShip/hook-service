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

# hs_urlencode STRING — percent-encode a string for safe use in a URL userinfo
# component. Bytes outside the unreserved set are encoded so an arbitrary
# secret can never corrupt the surrounding DSN.
hs_urlencode() {
	local LC_ALL=C string="$1" out="" char i
	for ((i = 0; i < ${#string}; i++)); do
		char="${string:i:1}"
		case "${char}" in
		[a-zA-Z0-9._~-]) out="${out}${char}" ;;
		*) printf -v char '%%%02X' "'${char}"; out="${out}${char}" ;;
		esac
	done
	printf '%s' "${out}"
}

# hs_pg_dsn USER PASSWORD HOSTPORT [DBNAME] — build a valid libpq URL DSN. The
# password is percent-encoded so any secret is safe inside the URL, and the DSN
# should be passed through hs_redact_dsn before it is ever logged.
hs_pg_dsn() {
	local user="$1" password="$2" hostport="$3" db="${4:-hookship}"
	printf 'postgres://%s:%s@%s/%s?sslmode=disable' \
		"$(hs_urlencode "${user}")" "$(hs_urlencode "${password}")" "${hostport}" "${db}"
}

# hs_compose_default_network PROJECT — the Compose default network name for a
# project, mirroring Docker Compose's "<project>_default" convention. Pure.
hs_compose_default_network() {
	printf '%s_default' "$1"
}

# hs_compose_network COMPOSE_DIR COMPOSE_FILE [SERVICE] — print the actual Docker
# network a running Compose service is attached to, so callers use the real
# project network instead of a hard-coded guess (the project name can change via
# `name:` or COMPOSE_PROJECT_NAME). Returns non-zero when the service container
# is not running, so the caller can tell "cannot probe" from "incompatible".
hs_compose_network() {
	local dir="$1" file="$2" service="${3:-postgres}" cid
	cid="$(cd -- "${dir}" && docker compose -f "${file}" ps -q "${service}" 2>/dev/null | head -n1)" || return 1
	[ -n "${cid}" ] || return 1
	docker inspect \
		-f '{{range $name, $_ := .NetworkSettings.Networks}}{{println $name}}{{end}}' \
		"${cid}" 2>/dev/null | sed '/^$/d' | head -n1
}

# hs_probe_migrate_compat CMD... — run a forward-only migration command and
# classify whether a code-only rollback is schema-safe. It exits:
#   0  schema compatible (the older code's migrate is a verified no-op)
#   1  schema INCOMPATIBLE — the database has a forward migration the code lacks;
#      migrate failed closed with "missing from source"
#   2  the probe could NOT run (unreachable database / infra error). This is
#      never reported as incompatible, so a rollback is neither wrongly blocked
#      nor wrongly allowed on an infrastructure failure.
# hookshipctl migrate fails closed with "missing from source" when the database
# is ahead of the code, which is exactly the signal this classifier keys on.
hs_probe_migrate_compat() {
	local out rc=0
	out="$("$@" 2>&1)" || rc=$?
	[ "${rc}" -eq 0 ] && return 0
	if printf '%s\n' "${out}" | grep -q 'missing from source'; then
		return 1
	fi
	hs_warn "schema compatibility probe could not run:"
	printf '%s\n' "${out}" >&2
	return 2
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
