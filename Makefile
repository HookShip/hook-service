SHELL := /bin/sh

GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf 'none')
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w \
	-X github.com/hookship/hook-service/internal/buildinfo.Version=$(VERSION) \
	-X github.com/hookship/hook-service/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/hookship/hook-service/internal/buildinfo.Date=$(BUILD_DATE)

.PHONY: all build check clean ci coverage diff-check docs-check fmt fmt-check gitleaks integration-image lint migration-check release-snapshot release-clean release-verify-test shellcheck smoke-deploy spec-validate test test-integration test-race test-scripts validate vet vulncheck

all: build

build:
	@mkdir -p bin
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/hookshipd ./cmd/hookshipd
	$(GO) build -trimpath -ldflags "$(LDFLAGS)" -o bin/hookshipctl ./cmd/hookshipctl

fmt:
	gofmt -w $$(find . -type f -name '*.go' -not -path './.git/*')

fmt-check:
	@test -z "$$(gofmt -l $$(find . -type f -name '*.go' -not -path './.git/*'))" || \
		{ echo "Go files need formatting; run 'make fmt'"; exit 1; }

test:
	$(GO) test ./...

test-integration:
	@test -n "$$TEST_DATABASE_URL" || \
		{ echo "TEST_DATABASE_URL is required for make test-integration" >&2; exit 1; }
	$(GO) test -count=1 -tags=integration \
		./internal/durability/postgres \
		./internal/backend/postgres \
		./internal/backend/relay \
		./internal/backend/objectstore

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

# lint runs golangci-lint with the pinned, high-signal linter set in
# .golangci.yml. It prefers a locally installed golangci-lint for a fast inner
# loop and otherwise falls back to a version-pinned `go run`, so CI (which has
# no preinstalled binary) is deterministic. Formatting stays owned by gofmt.
GOLANGCI_LINT_VERSION ?= v2.12.2
lint:
	@if command -v golangci-lint >/dev/null 2>&1; then \
		golangci-lint run ./...; \
	else \
		$(GO) run github.com/golangci/golangci-lint/v2/cmd/golangci-lint@$(GOLANGCI_LINT_VERSION) run ./...; \
	fi

# vulncheck fails on any known, reachable vulnerability in the module or its
# dependencies. It is pinned and requires network access (the vulnerability
# database), so it runs in `ci` rather than the offline-friendly `check` lane,
# mirroring how the network-dependent gitleaks scan is kept separate.
GOVULNCHECK_VERSION ?= v1.1.4
vulncheck:
	$(GO) run golang.org/x/vuln/cmd/govulncheck@$(GOVULNCHECK_VERSION) ./...

validate:
	./scripts/validate-repo.sh

docs-check:
	python3 scripts/validate-docs.py

spec-validate:
	$(GO) test -count=1 ./internal/specvalidation

migration-check:
	$(GO) test -count=1 -run 'TestMigrationManifest' ./internal/durability/postgres

# shellcheck lints every shell script with `-x` so sourced libraries (lib.sh)
# are followed. The operational recovery scripts are safety-critical, so this is
# part of the standard check.
SHELL_SOURCES := $(shell find deploy/scripts scripts -name '*.sh') deploy/scripts/test/fakebin/docker

shellcheck:
	@command -v shellcheck >/dev/null 2>&1 || \
		{ echo "shellcheck is required for make shellcheck" >&2; exit 1; }
	shellcheck -x $(SHELL_SOURCES)

# test-scripts runs the hermetic shell tests (pure lib.sh helpers plus rollback
# probe classification with a fake docker). It needs only bash, so it runs in
# the ordinary check lane alongside the Go tests.
test-scripts:
	./deploy/scripts/test/run.sh

# coverage enforces the documented per-package coverage floors. It always checks
# unit coverage; when TEST_DATABASE_URL (and, for the object-store path, the
# MINIO_TEST_* variables) are set it also enforces the durability/backend
# integration coverage floors. It fails on any regression below a floor.
coverage:
	./scripts/coverage.sh

# smoke-deploy runs the real disposable-Postgres recovery drills. It requires
# docker and the PostgreSQL client tools and is kept out of `check`; run it
# locally or in an integration lane where those resources are available.
smoke-deploy:
	./deploy/scripts/test/smoke_compose_network.sh
	./deploy/scripts/restore-drill.sh

diff-check:
	git diff --check

release-snapshot:
	$(GO) run ./scripts/release snapshot

release-clean:
	rm -rf dist

# integration-image builds a local, credential-free OCI/docker archive artifact
# (dist/integration/) that an adjacent consumer can load and verify without any
# registry pull. It requires Docker. The real release path stays digest-pinned.
integration-image:
	./scripts/build-integration-image.sh

release-verify-test:
	./scripts/verify-release-tag.test.sh

gitleaks:
	@command -v gitleaks >/dev/null 2>&1 || \
		{ echo "gitleaks is required for make gitleaks" >&2; exit 1; }
	gitleaks detect --no-banner --redact --source .

check: fmt-check validate docs-check spec-validate migration-check shellcheck test-scripts vet lint test test-race build diff-check

ci: check vulncheck

clean:
	rm -rf bin
