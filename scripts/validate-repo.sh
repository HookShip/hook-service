#!/bin/sh
set -eu

repository_root=$(CDPATH='' cd -- "$(dirname "$0")/.." && pwd)
cd "$repository_root"

required_files="
CODE_OF_CONDUCT.md
CONTRIBUTING.md
CHANGELOG.md
GOVERNANCE.md
ROADMAP.md
Dockerfile
LICENSE
Makefile
NOTICE
README.md
requirements-spec.txt
SECURITY.md
SUPPORT.md
THIRD_PARTY_NOTICES.md
deploy/README.md
deploy/hookshipd.env.example
deploy/assignments.example.yaml
deploy/systemd/hookshipd.service
deploy/systemd/hookship-migrate.service
deploy/systemd/hookship-backup.service
deploy/systemd/hookship-backup.timer
deploy/scripts/install.sh
deploy/scripts/uninstall.sh
deploy/scripts/compose-up.sh
deploy/scripts/backup.sh
deploy/scripts/restore.sh
deploy/scripts/restore-drill.sh
deploy/scripts/upgrade.sh
deploy/scripts/rollback.sh
deploy/scripts/rotate-payload-key.sh
deploy/scripts/backup-age.sh
deploy/compose/compose.build.yaml
deploy/compose/compose.pull.yaml
docs/architecture.md
docs/compatibility-matrix.md
docs/configuration.md
docs/deployment.md
docs/durability.md
docs/evaluation.md
docs/development.md
docs/examples/durability.env.example
docs/operations.md
docs/provenance.md
docs/releasing.md
docs/troubleshooting.md
docs/runbooks/README.md
docs/runbooks/backup-restore.md
docs/runbooks/restore-drill.md
docs/runbooks/upgrade-rollback.md
docs/runbooks/key-rotation.md
docs/runbooks/postgres-recovery.md
docs/runbooks/minio-recovery.md
docs/runbooks/log-rotation.md
docs/adr/0008-release-and-packaging.md
examples/README.md
internal/release/release.go
scripts/release/main.go
scripts/build-integration-image.sh
scripts/validate-docs.py
scripts/verify-release-tag.sh
.github/workflows/release.yml
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
