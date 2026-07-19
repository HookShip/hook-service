#!/bin/sh
set -eu

repository_root=$(CDPATH= cd -- "$(dirname "$0")/.." && pwd)
cd "$repository_root"

required_files="
CODE_OF_CONDUCT.md
CONTRIBUTING.md
Dockerfile
LICENSE
Makefile
NOTICE
README.md
SECURITY.md
SUPPORT.md
THIRD_PARTY_NOTICES.md
docs/architecture.md
docs/configuration.md
docs/development.md
docs/operations.md
docs/provenance.md
go.mod
spec/README.md
"

for file in $required_files; do
	if [ ! -s "$file" ]; then
		echo "required file is missing or empty: $file" >&2
		exit 1
	fi
done

if ! grep -qx 'module github.com/hookship/hook-service' go.mod; then
	echo "go.mod does not declare the HookShip module path" >&2
	exit 1
fi

if ! grep -q 'Apache License' LICENSE; then
	echo "LICENSE does not contain the Apache License" >&2
	exit 1
fi

if ! grep -q 'No WAAS source code has been imported' THIRD_PARTY_NOTICES.md; then
	echo "WAAS provenance boundary is missing" >&2
	exit 1
fi

go mod verify
go list ./... >/dev/null

echo "repository validation passed"
