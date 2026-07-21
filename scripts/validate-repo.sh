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
requirements-spec.txt
SECURITY.md
SUPPORT.md
THIRD_PARTY_NOTICES.md
docs/architecture.md
docs/configuration.md
docs/durability.md
docs/development.md
docs/examples/durability.env.example
docs/operations.md
docs/provenance.md
go.mod
go.sum
internal/durability/postgres/migrations/000001_durability_v1.up.sql
internal/durability/postgres/migrations/README.md
internal/durability/postgres/migrations/checksums.txt
requirements-spec.txt
scripts/validate-jsonschema.py
scripts/validate-backend-capabilities.py
spec/README.md
spec/backend-capabilities/v1/README.md
spec/backend-capabilities/v1/schema.json
spec/backend-capabilities/v1/checksums.txt
spec/backend-capabilities/v1/fixtures/memory.json
spec/backend-capabilities/v1/fixtures/postgres.json
spec/backend-capabilities/v1/fixtures/postgres-redis.json
spec/backend-capabilities/v1/fixtures/postgres-kafka.json
spec/backend-capabilities/v1/fixtures/kafka-native-ledger.json
spec/v1/README.md
spec/v1/compatibility.md
spec/v1/configuration.md
spec/v1/integration.md
spec/v1/openapi.json
spec/v1/protocol.md
spec/v1/schema-registry.json
spec/v1/signing.md
spec/v1/checksums.txt
spec/v1/fixtures/accepted.json
spec/v1/fixtures/configuration-vectors.json
spec/v1/fixtures/conflict.json
spec/v1/fixtures/duplicate.json
spec/v1/fixtures/fleet-accepted.json
spec/v1/fixtures/fleet-applied.json
spec/v1/fixtures/fleet-rejected.json
spec/v1/fixtures/not-found-foreign.json
spec/v1/fixtures/not-found-unknown.json
spec/v1/fixtures/problem-responses.json
spec/v1/fixtures/rejected.json
spec/v1/fixtures/signing-vectors.json
spec/v1/fixtures/status.json
spec/v1/fixtures/timestamp-vectors.json
spec/v1/schemas/configuration-bundle.schema.json
spec/v1/schemas/configuration-vectors.schema.json
spec/v1/schemas/event-ingest-receipt.schema.json
spec/v1/schemas/event-ingest-status.schema.json
spec/v1/schemas/fleet-registration.schema.json
spec/v1/schemas/golden-http-outcome.schema.json
spec/v1/schemas/problem-details.schema.json
spec/v1/schemas/problem-response-vectors.schema.json
spec/v1/schemas/signing-vectors.schema.json
spec/v1/schemas/timestamp-vectors.schema.json
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
