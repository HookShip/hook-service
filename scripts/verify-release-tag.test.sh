#!/usr/bin/env bash
#
# verify-release-tag.test.sh — self-contained tests for verify-release-tag.sh.
#
# The fail-closed cases (missing tag, lightweight tag, missing key) run
# everywhere. The signature cases (good key, wrong key, unsigned tag) run only
# when gpg is available, so the same tests exercise the trusted-key path in CI.
#
# Usage: scripts/verify-release-tag.test.sh
set -euo pipefail

SCRIPT_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
VERIFY="${SCRIPT_DIR}/verify-release-tag.sh"

pass=0
fail=0
note() { printf '  %s\n' "$*"; }
ok() { pass=$((pass + 1)); printf 'PASS %s\n' "$*"; }
bad() { fail=$((fail + 1)); printf 'FAIL %s\n' "$*"; }

expect_fail() {
	local desc="$1"; shift
	if "$@" >/dev/null 2>&1; then bad "${desc} (expected failure, got success)"; else ok "${desc}"; fi
}
expect_pass() {
	local desc="$1"; shift
	if "$@" >/dev/null 2>&1; then ok "${desc}"; else bad "${desc} (expected success, got failure)"; fi
}

WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT

REPO="${WORK}/repo"
git init -q "${REPO}"
git -C "${REPO}" config user.email test@example.invalid
git -C "${REPO}" config user.name "Test"
git -C "${REPO}" commit -q --allow-empty -m "initial"
git -C "${REPO}" tag v0.0.1-light                         # lightweight
git -C "${REPO}" tag -a -m "unsigned" v0.0.1-unsigned      # annotated, unsigned

run() { git -C "${REPO}" "$@"; }
verify_in_repo() { ( cd "${REPO}" && env "$@" "${VERIFY}" ); }

echo "== fail-closed cases (no gpg required) =="
# No tag and no GITHUB_REF_NAME.
expect_fail "refuses when no tag is given" \
	env -u GITHUB_REF_NAME bash -c "cd '${REPO}' && '${VERIFY}'"
# Lightweight tag is rejected.
expect_fail "refuses a lightweight tag" \
	env RELEASE_TAG_GPG_PUBLIC_KEY=dummy bash -c "cd '${REPO}' && '${VERIFY}' v0.0.1-light"
# Annotated but no trusted key material.
expect_fail "refuses when no trusted key is provided" \
	env -u RELEASE_TAG_GPG_PUBLIC_KEY bash -c "cd '${REPO}' && '${VERIFY}' v0.0.1-unsigned"

if ! command -v gpg >/dev/null 2>&1; then
	note "gpg not available; skipping signature cases (they run in CI)"
	printf '\n%d passed, %d failed\n' "${pass}" "${fail}"
	[ "${fail}" -eq 0 ]
	exit $?
fi

echo "== signature cases (gpg present) =="
# Ephemeral trusted signer.
TRUSTED_HOME="${WORK}/trusted-gpg"; mkdir -p "${TRUSTED_HOME}"; chmod 700 "${TRUSTED_HOME}"
GNUPGHOME="${TRUSTED_HOME}" gpg --batch --pinentry-mode loopback --passphrase '' \
	--quick-generate-key "HookShip Release <release@example.invalid>" default default 0 >/dev/null 2>&1
trusted_key="$(GNUPGHOME="${TRUSTED_HOME}" gpg --list-secret-keys --with-colons | awk -F: '/^fpr:/{print $10; exit}')"
GNUPGHOME="${TRUSTED_HOME}" gpg --batch --armor --export "${trusted_key}" >"${WORK}/trusted.asc"

# A different, untrusted signer.
OTHER_HOME="${WORK}/other-gpg"; mkdir -p "${OTHER_HOME}"; chmod 700 "${OTHER_HOME}"
GNUPGHOME="${OTHER_HOME}" gpg --batch --pinentry-mode loopback --passphrase '' \
	--quick-generate-key "Someone Else <else@example.invalid>" default default 0 >/dev/null 2>&1
other_key="$(GNUPGHOME="${OTHER_HOME}" gpg --list-secret-keys --with-colons | awk -F: '/^fpr:/{print $10; exit}')"

# Sign a tag with the trusted key.
GNUPGHOME="${TRUSTED_HOME}" git -C "${REPO}" -c "user.signingkey=${trusted_key}" \
	tag -s -m "signed" v0.0.2-trusted
# Sign a tag with the other key.
GNUPGHOME="${OTHER_HOME}" git -C "${REPO}" -c "user.signingkey=${other_key}" \
	tag -s -m "signed by other" v0.0.3-other

expect_pass "verifies a tag signed by the trusted key" \
	env "RELEASE_TAG_GPG_PUBLIC_KEY=$(cat "${WORK}/trusted.asc")" \
	bash -c "cd '${REPO}' && '${VERIFY}' v0.0.2-trusted"

expect_fail "rejects a tag signed by an untrusted key" \
	env "RELEASE_TAG_GPG_PUBLIC_KEY=$(cat "${WORK}/trusted.asc")" \
	bash -c "cd '${REPO}' && '${VERIFY}' v0.0.3-other"

expect_fail "rejects an unsigned annotated tag even with a trusted key" \
	env "RELEASE_TAG_GPG_PUBLIC_KEY=$(cat "${WORK}/trusted.asc")" \
	bash -c "cd '${REPO}' && '${VERIFY}' v0.0.1-unsigned"

printf '\n%d passed, %d failed\n' "${pass}" "${fail}"
[ "${fail}" -eq 0 ]
