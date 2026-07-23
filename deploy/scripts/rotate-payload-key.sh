#!/usr/bin/env bash
#
# rotate-payload-key.sh — rotate the payload encryption key material.
#
# Two rotation stories, matching how hook-service actually seals payloads:
#
#   kms-rewrap  (RECOMMENDED, zero-downtime)
#     Re-wraps the SAME data encryption key (DEK) under a new KMS key. Because
#     the DEK plaintext is unchanged, the derived key version is unchanged and
#     every previously sealed payload stays decryptable. Updates the env file
#     and restarts. Requires the AWS CLI and KMS access.
#
#   static-check  (SAFETY GATE for static-key rotation)
#     The static provider holds ONE active key with no multi-key resolver, so
#     changing HOOKSHIP_PAYLOAD_KEY to new bytes makes payloads sealed under the
#     old key undecryptable. This mode reports whether any live ciphertext still
#     depends on the current key. Rotating a static key is safe ONLY when it
#     reports zero live payloads; otherwise migrate to the KMS provider (or wait
#     for retention to reclaim terminal payloads) — see the runbook.
#
# Usage:
#   deploy/scripts/rotate-payload-key.sh kms-rewrap --new-key-id KEY [--env-file FILE]
#   deploy/scripts/rotate-payload-key.sh static-check [--dsn DSN] [--env-file FILE]
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/lib.sh
. "${SCRIPT_DIR}/lib.sh"

MODE="${1:-}"
shift || true
ENV_FILE="${HOOKSHIP_ENV_FILE:-/etc/hookship/hookshipd.env}"
NEW_KEY_ID=""
DSN_ARG=""

while [ "$#" -gt 0 ]; do
	case "$1" in
	--new-key-id) NEW_KEY_ID="${2:?}"; shift 2 ;;
	--env-file) ENV_FILE="${2:?}"; shift 2 ;;
	--dsn) DSN_ARG="${2:?}"; shift 2 ;;
	-h | --help) sed -n '2,27p' "$0"; exit 0 ;;
	*) hs_die "unknown argument: $1" ;;
	esac
done
export HOOKSHIP_ENV_FILE="${ENV_FILE}"

env_value() { sed -n "s/^$1=//p" "${ENV_FILE}" 2>/dev/null | tail -n1; }

case "${MODE}" in
kms-rewrap)
	hs_require aws
	[ -n "${NEW_KEY_ID}" ] || hs_die "--new-key-id KEY is required"
	[ -f "${ENV_FILE}" ] || hs_die "env file not found: ${ENV_FILE}"
	current="$(env_value HOOKSHIP_KMS_WRAPPED_DATA_KEY)"
	[ -n "${current}" ] || hs_die "HOOKSHIP_KMS_WRAPPED_DATA_KEY is not set; is the KMS provider configured?"

	region="$(env_value HOOKSHIP_KMS_REGION)"
	tmp="$(mktemp)"; trap 'rm -f "${tmp}"' EXIT
	printf '%s' "${current}" | base64 --decode >"${tmp}" 2>/dev/null || hs_die "current wrapped key is not valid base64"

	hs_log "re-wrapping the DEK under ${NEW_KEY_ID} (DEK plaintext unchanged)"
	new_wrapped="$(aws kms re-encrypt \
		${region:+--region "${region}"} \
		--ciphertext-blob "fileb://${tmp}" \
		--destination-key-id "${NEW_KEY_ID}" \
		--query CiphertextBlob --output text)"
	[ -n "${new_wrapped}" ] || hs_die "KMS re-encrypt returned no ciphertext"

	hs_log "updating ${ENV_FILE}"
	tmpenv="$(mktemp)"
	sed -e "s#^HOOKSHIP_KMS_WRAPPED_DATA_KEY=.*#HOOKSHIP_KMS_WRAPPED_DATA_KEY=${new_wrapped}#" \
		-e "s#^HOOKSHIP_KMS_KEY_ID=.*#HOOKSHIP_KMS_KEY_ID=${NEW_KEY_ID}#" \
		"${ENV_FILE}" >"${tmpenv}"
	cat "${tmpenv}" >"${ENV_FILE}"
	rm -f "${tmpenv}"

	hs_log "rewrap complete. Restart the service to pick up the new wrapped key:"
	hs_log "  systemctl restart hookshipd.service   (or your Compose equivalent)"
	hs_log "Historical payloads remain decryptable because the DEK is unchanged."
	;;
static-check)
	hs_require psql
	DSN="$(hs_resolve_dsn "${DSN_ARG}")"
	[ -n "${DSN}" ] || hs_die "no DSN (pass --dsn or set HOOKSHIP_DATABASE_URL / ${ENV_FILE})"
	hs_log "checking for live ciphertext that depends on the current key"
	rows="$(psql "${DSN}" -tA -F'|' -c \
		"SELECT key_version, count(*) FROM payload_records WHERE deleted_at IS NULL AND (ciphertext IS NOT NULL OR final_object_key IS NOT NULL) GROUP BY key_version ORDER BY key_version")"
	if [ -z "${rows}" ]; then
		printf 'result: SAFE — no live payloads depend on the current static key.\n'
		printf 'You may set a new HOOKSHIP_PAYLOAD_KEY and restart.\n'
		exit 0
	fi
	printf 'result: NOT SAFE — live ciphertext still depends on these key versions:\n'
	printf '  key_version|live_payloads\n'
	printf '%s\n' "${rows}" | sed 's/^/  /'
	printf 'Rotating the static key now would make these payloads undecryptable.\n'
	printf 'Use kms-rewrap, or wait for retention to reclaim terminal payloads.\n'
	exit 1
	;;
"" | -h | --help) sed -n '2,27p' "$0"; exit 0 ;;
*) hs_die "unknown mode '${MODE}' (expected kms-rewrap or static-check)" ;;
esac
