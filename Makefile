.DEFAULT_GOAL := build

GOLANGCI_LINT_VERSION ?= v2.12.2
SQLC_VERSION ?= v1.31.1
GOLANGCI_LINT ?= $(or $(shell command -v golangci-lint 2>/dev/null),$(CURDIR)/bin/golangci-lint)
GOLANGCI_LINT_CACHE := $(CURDIR)/tmp/golangci-lint-cache
SQLC ?= $(CURDIR)/bin/sqlc

.PHONY: test test-fast build build-cli build-daemon run-daemon run-cli fmt lint codegen verify verify-generated smoke smoke-noop smoke-podman build-smoke-runner-image check-podman

SMOKE_PODMAN_IMAGE ?= rascal-runner-smoke-codex:latest
SMOKE_RUNNER_CONTEXT := $(CURDIR)/tmp/smoke-runner

test: codegen
	go test ./...

test-fast:
	go test ./...

fmt:
	go fmt ./...

lint: codegen $(GOLANGCI_LINT)
	mkdir -p "$(GOLANGCI_LINT_CACHE)"
	GOLANGCI_LINT_CACHE="$(GOLANGCI_LINT_CACHE)" $(GOLANGCI_LINT) run

verify:
	@before="$$(git status --porcelain)"; \
	$(MAKE) lint; \
	$(MAKE) test; \
	VERIFY_GIT_STATUS_BEFORE="$$before" $(MAKE) verify-generated

smoke: smoke-noop smoke-podman

smoke-noop: build-daemon
	go test -tags=smoke ./smoke -run TestSmokeNoop -count=1

smoke-podman: build-daemon build-smoke-runner-image
	SMOKE_PODMAN_IMAGE="$(SMOKE_PODMAN_IMAGE)" go test -tags=smoke ./smoke -run TestSmokePodman -count=1

verify-generated:
	@before="$${VERIFY_GIT_STATUS_BEFORE-}"; \
	after="$$(git status --porcelain)"; \
	if [ -z "$$before" ]; then \
		git diff --exit-code; \
		exit $$?; \
	fi; \
	if [ "$$before" = "$$after" ]; then \
		exit 0; \
	fi; \
	echo "verification changed the working tree; commit generated or derived files" >&2; \
	git diff --exit-code

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

check-podman:
	@podman info >/dev/null 2>&1 || (echo "podman is not available; start Podman before running smoke-podman" >&2; exit 1)

build-smoke-runner-image: codegen check-podman
	rm -rf "$(SMOKE_RUNNER_CONTEXT)"
	mkdir -p "$(SMOKE_RUNNER_CONTEXT)"
	cp runner/Dockerfile "$(SMOKE_RUNNER_CONTEXT)/Dockerfile"
	cp runner/entrypoint.sh "$(SMOKE_RUNNER_CONTEXT)/entrypoint.sh"
	cp -R runner/smoke "$(SMOKE_RUNNER_CONTEXT)/smoke"
	GOOS=linux GOARCH="$$(go env GOARCH)" CGO_ENABLED=0 go build -o "$(SMOKE_RUNNER_CONTEXT)/rascal-runner" ./cmd/rascal-runner
	podman build --quiet --target smoke-codex-runner -t "$(SMOKE_PODMAN_IMAGE)" "$(SMOKE_RUNNER_CONTEXT)"

run-daemon:
	go run ./cmd/rascald

run-cli:
	go run ./cmd/rascal
