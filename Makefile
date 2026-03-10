.DEFAULT_GOAL := build

GOLANGCI_LINT_VERSION ?= v2.11.2
GOLANGCI_LINT := $(CURDIR)/bin/golangci-lint
GOLANGCI_LINT_CACHE := $(CURDIR)/tmp/golangci-lint-cache
FAST_TEST_PACKAGE_FILTER := ^github.com/rtzll/rascal/(cmd/rascald|internal/deploy)$$

.PHONY: test test-fast build build-cli build-daemon run-daemon run-cli fmt lint codegen

test: codegen
	go test ./...

test-fast:
	# Fast contributor loop: skip the slower daemon/deploy integration-style suites.
	# Use `make test` for full verification across every package.
	go test $$(go list ./... | grep -vE '$(FAST_TEST_PACKAGE_FILTER)')

fmt:
	gofmt -w cmd internal

lint: codegen $(GOLANGCI_LINT)
	mkdir -p "$(GOLANGCI_LINT_CACHE)"
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" $(GOLANGCI_LINT) run

build: build-cli build-daemon

codegen:
	CGO_ENABLED=0 go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

$(GOLANGCI_LINT):
	mkdir -p "$(dir $(GOLANGCI_LINT))"
	curl -sSfL https://golangci-lint.run/install.sh | sh -s -- -b "$(dir $(GOLANGCI_LINT))" "$(GOLANGCI_LINT_VERSION)"

build-cli: codegen
	go build -o bin/rascal ./cmd/rascal

build-daemon: codegen
	go build -o bin/rascald ./cmd/rascald

run-daemon:
	go run ./cmd/rascald

run-cli:
	go run ./cmd/rascal
