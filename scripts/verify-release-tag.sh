#!/usr/bin/env bash
#
# verify-release-tag.sh — fail-closed verification that a release tag is
# annotated AND carries a good cryptographic signature from a trusted key.
#
# The org RELEASE_POLICY requires signed, verifiable release tags. This script
# refuses to succeed unless the tag is annotated and produces a GnuPG GOODSIG
# from a key provided out of band (never committed): the release workflow passes
# it as the protected release-environment secret RELEASE_TAG_GPG_PUBLIC_KEY.
#
# Usage:
#   RELEASE_TAG_GPG_PUBLIC_KEY="$(cat trusted.asc)" \
#     scripts/verify-release-tag.sh [<tag>]
#   scripts/verify-release-tag.sh --pubkey-file trusted.asc <tag>
#
# The tag defaults to GITHUB_REF_NAME. The public key is imported into an
# isolated, throwaway keyring so ONLY that key can satisfy verification.
#
# Exit codes: 0 verified; non-zero on any failure (missing tag, lightweight
# tag, missing/empty key, missing gpg, or an unsigned/untrusted/expired sig).
set -euo pipefail

die() { printf 'verify-release-tag: %s\n' "$*" >&2; exit 1; }

TAG=""
PUBKEY_FILE=""
while [ "$#" -gt 0 ]; do
	case "$1" in
	--pubkey-file) PUBKEY_FILE="${2:?--pubkey-file requires a value}"; shift 2 ;;
	--tag) TAG="${2:?--tag requires a value}"; shift 2 ;;
	-h | --help) sed -n '2,22p' "$0"; exit 0 ;;
	-*) die "unknown argument: $1" ;;
	*) TAG="$1"; shift ;;
	esac
done

TAG="${TAG:-${GITHUB_REF_NAME:-}}"
[ -n "${TAG}" ] || die "no tag given (pass a tag or set GITHUB_REF_NAME)"

# 1. Must be an annotated tag object (not a lightweight tag).
type="$(git cat-file -t "${TAG}" 2>/dev/null || true)"
[ "${type}" = "tag" ] || die "tag '${TAG}' is not an annotated tag (got '${type:-none}')"

# 2. Trusted public key material must be provided out of band.
if [ -n "${PUBKEY_FILE}" ]; then
	[ -s "${PUBKEY_FILE}" ] || die "public key file '${PUBKEY_FILE}' is missing or empty"
	key_material="$(cat "${PUBKEY_FILE}")"
else
	key_material="${RELEASE_TAG_GPG_PUBLIC_KEY:-}"
fi
[ -n "${key_material}" ] || die "no trusted public key (set RELEASE_TAG_GPG_PUBLIC_KEY or --pubkey-file); refusing to publish an unverifiable tag"

# 3. gpg is required to check the signature.
command -v gpg >/dev/null 2>&1 || die "gpg is required to verify the tag signature"

# 4. Import the trusted key into an isolated, throwaway keyring so only that key
#    can produce a GOODSIG.
GNUPGHOME="$(mktemp -d)"
export GNUPGHOME
chmod 700 "${GNUPGHOME}"
trap 'rm -rf "${GNUPGHOME}"' EXIT
printf '%s\n' "${key_material}" | gpg --batch --quiet --import 2>/dev/null ||
	die "the provided public key material could not be imported"

# 5. Verify. Require a GOODSIG (rejects unsigned, BADSIG, unknown key, and
#    expired/revoked keys, which report EXPKEYSIG/REVKEYSIG instead).
status="$(git verify-tag --raw "${TAG}" 2>&1 || true)"
if ! printf '%s\n' "${status}" | grep -q 'GOODSIG'; then
	printf '%s\n' "${status}" >&2
	die "tag '${TAG}' has no good signature from the trusted release key"
fi

printf 'verify-release-tag: %s is annotated and signed by the trusted release key\n' "${TAG}"
