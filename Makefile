SHELL := /bin/sh

GO ?= go
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || printf 'dev')
COMMIT ?= $(shell git rev-parse --short=12 HEAD 2>/dev/null || printf 'none')
BUILD_DATE ?= $(shell date -u '+%Y-%m-%dT%H:%M:%SZ')
LDFLAGS := -s -w \
	-X github.com/hookship/hook-service/internal/buildinfo.Version=$(VERSION) \
	-X github.com/hookship/hook-service/internal/buildinfo.Commit=$(COMMIT) \
	-X github.com/hookship/hook-service/internal/buildinfo.Date=$(BUILD_DATE)

.PHONY: all build check clean ci fmt fmt-check spec-validate test test-race validate vet

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

test-race:
	$(GO) test -race ./...

vet:
	$(GO) vet ./...

validate:
	./scripts/validate-repo.sh

spec-validate:
	$(GO) test -count=1 ./internal/specvalidation

check: fmt-check validate spec-validate vet test test-race build

ci: check

clean:
	rm -rf bin
