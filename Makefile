.DEFAULT_GOAL := build

.PHONY: test test-fast build build-cli build-daemon run-daemon run-cli fmt codegen

test: codegen
	go test ./...

test-fast:
	go test ./...

fmt:
	gofmt -w cmd internal

build: build-cli build-daemon

codegen:
	go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.30.0 generate

build-cli: codegen
	go build -o bin/rascal ./cmd/rascal

build-daemon: codegen
	go build -o bin/rascald ./cmd/rascald

run-daemon:
	go run ./cmd/rascald

run-cli:
	go run ./cmd/rascal
