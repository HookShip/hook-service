#!/usr/bin/env bash
#
# coverage_gate_test.sh — prove the coverage gate survives a fresh clone.
#
# scripts/coverage.sh was previously swallowed by an over-broad .gitignore
# pattern, so a fresh clone was missing it while the Makefile and CI invoked it.
# This test asserts the script is tracked (present in the fresh-checkout content
# that `git ls-files` reports) and executable, and that it executes end-to-end
# using a fake `go` so the gate logic runs deterministically and fast.
set -euo pipefail

TEST_DIR="$(cd -- "$(dirname -- "$0")" && pwd)"
# shellcheck source=deploy/scripts/test/assert.sh
. "${TEST_DIR}/assert.sh"
REPO_ROOT="$(cd -- "${TEST_DIR}/../../.." && pwd)"
cd "${REPO_ROOT}"

# 1. Tracked: git ls-files reports the exact fresh-checkout content, so the
#    script must appear there (not merely on the working-tree disk).
tracked="$(git ls-files scripts/coverage.sh)"
assert_eq "scripts/coverage.sh" "${tracked}" "coverage.sh must be tracked (fresh clone would have it)"

# 2. Tracked executable: mode 100755 in the index.
mode="$(git ls-files -s scripts/coverage.sh | awk '{print $1}')"
assert_eq "100755" "${mode}" "coverage.sh must be tracked as executable"

# 3. make coverage executes. The Makefile target is `./scripts/coverage.sh`; run
#    it with a fake go that emits a coverage line, so the whole gate runs without
#    executing the real (slow) test suite.
WORK="$(mktemp -d)"
trap 'rm -rf "${WORK}"' EXIT
cat >"${WORK}/go" <<'FAKE'
#!/usr/bin/env bash
# Fake go for the coverage-gate test: emit one coverage line for `go test`.
printf 'ok  pkg 0.01s coverage: %s%% of statements\n' "${FAKE_COV:-100.0}"
FAKE
chmod +x "${WORK}/go"

rc=0
GO="${WORK}/go" ./scripts/coverage.sh >/dev/null 2>&1 || rc=$?
assert_status 0 "${rc}" "coverage gate passes and executes at high coverage"

# Teeth: below the floors the gate fails.
rc=0
FAKE_COV=1.0 GO="${WORK}/go" ./scripts/coverage.sh >/dev/null 2>&1 || rc=$?
assert_status 1 "${rc}" "coverage gate fails below floors"

assert_done
