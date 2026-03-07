.DEFAULT_GOAL := build

GO ?= go

BUILDINFO_PKG := github.com/rtzll/rascal/internal/buildinfo
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo "dev")
COMMIT ?= $(shell git rev-parse --short HEAD 2>/dev/null || echo "unknown")
DATE ?= $(shell date -u +"%Y-%m-%dT%H:%M:%SZ")
BUILD_LDFLAGS := -X $(BUILDINFO_PKG).Version=$(VERSION) -X $(BUILDINFO_PKG).Commit=$(COMMIT) -X $(BUILDINFO_PKG).Date=$(DATE)

.PHONY: test test-fast build build-cli build-daemon run-daemon run-cli fmt codegen

test: codegen
	$(GO) test ./...

test-fast:
	$(GO) test ./...

fmt:
	gofmt -w cmd internal

build: build-cli build-daemon

codegen:
	CGO_ENABLED=0 $(GO) run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

build-cli: codegen
	$(GO) build -ldflags "$(BUILD_LDFLAGS)" -o bin/rascal ./cmd/rascal

build-daemon: codegen
	$(GO) build -ldflags "$(BUILD_LDFLAGS)" -o bin/rascald ./cmd/rascald

run-daemon:
	$(GO) run ./cmd/rascald

run-cli:
	$(GO) run ./cmd/rascal
