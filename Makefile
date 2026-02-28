.PHONY: test build build-cli build-daemon run-daemon run-cli fmt

test:
	go test ./...

fmt:
	gofmt -w cmd internal

build: build-cli build-daemon

build-cli:
	go build -o bin/rascal ./cmd/rascal

build-daemon:
	go build -o bin/rascald ./cmd/rascald

run-daemon:
	go run ./cmd/rascald

run-cli:
	go run ./cmd/rascal
