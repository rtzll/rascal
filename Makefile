.DEFAULT_GOAL := build

GOLANGCI_LINT_VERSION ?= v2.11.2
SQLC_VERSION ?= v1.30.0
GOLANGCI_LINT ?= $(or $(shell command -v golangci-lint 2>/dev/null),$(CURDIR)/bin/golangci-lint)
GOLANGCI_LINT_CACHE := $(CURDIR)/tmp/golangci-lint-cache
SQLC ?= $(or $(shell command -v sqlc 2>/dev/null),$(CURDIR)/bin/sqlc)

.PHONY: test test-fast build build-cli build-daemon run-daemon run-cli fmt lint codegen

test: codegen
	go test ./...

test-fast:
	go test ./...

fmt:
	go fmt ./...

lint: codegen $(GOLANGCI_LINT)
	mkdir -p "$(GOLANGCI_LINT_CACHE)"
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" $(GOLANGCI_LINT) run

build: build-cli build-daemon

codegen: $(SQLC)
	CGO_ENABLED=0 $(SQLC) generate

$(SQLC):
	mkdir -p "$(dir $(SQLC))"
	GOBIN="$(dir $(SQLC))" CGO_ENABLED=0 go install github.com/sqlc-dev/sqlc/cmd/sqlc@$(SQLC_VERSION)

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
