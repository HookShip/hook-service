#!/bin/sh
# Demonstrates the HookShip delivery vertical slice end to end:
# accept -> signed delivery -> retry/DLQ -> payload-free status.
#
# Usage:
#   scripts/demo.sh [memory|evaluation|redis|kafka] [retry]
#
# The optional "retry" argument makes the local sink reject the first two
# deliveries so the client observes a retry before success. Without it the
# delivery succeeds on the first attempt.
set -eu

profile="${1:-memory}"
mode="${2:-}"

case "$profile" in
	memory | evaluation | redis | kafka) ;;
	*)
		echo "unknown profile: $profile (expected memory|evaluation|redis|kafka)" >&2
		exit 2
		;;
esac

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repository_root"

fail_first=0
if [ "$mode" = "retry" ]; then
	fail_first=2
	echo "retry mode: the local sink will reject the first $fail_first deliveries"
fi

echo "==> starting the '$profile' profile"
SINK_FAIL_FIRST="$fail_first" docker compose --profile "$profile" up --build -d

cleanup_hint() {
	echo ""
	echo "Tear down with: docker compose --profile $profile down -v"
}
trap cleanup_hint EXIT

echo "==> waiting for readiness on http://127.0.0.1:8080/readyz"
attempt=0
until curl -fsS http://127.0.0.1:8080/readyz >/dev/null 2>&1; do
	attempt=$((attempt + 1))
	if [ "$attempt" -gt 90 ]; then
		echo "hookshipd did not become ready" >&2
		docker compose --profile "$profile" ps
		exit 1
	fi
	sleep 1
done

echo "==> readiness:"
curl -fsS http://127.0.0.1:8080/readyz
echo ""

echo "==> submitting a signed event and reading payload-free status"
go run ./examples/evaluation-client -url=http://127.0.0.1:8080

echo ""
echo "==> local sink log (verified Standard Webhooks deliveries):"
docker compose --profile "$profile" logs local-sink 2>/dev/null | tail -n 10 || true
