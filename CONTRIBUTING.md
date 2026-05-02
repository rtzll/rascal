# Contributing

This guide covers the fastest path to make and verify a typical change locally.

## Prerequisites

- Go 1.26 (matches `go.mod`)
- `make`
- `curl` (used by `make lint` to install `golangci-lint` into `./bin`)

You do not need a separate `sqlc` install. `make codegen` installs the pinned
version into `./bin/sqlc` and runs it from there.

## Local workflow

Run this command from the repository root:

```bash
make verify
```

- `make verify` mirrors CI: it runs `make lint`, `make test`, and then
  checks whether verification changed the working tree, so local edits do not
  fail verification unless lint/test/code generation introduce extra diffs.
- `make lint` runs SQLC code generation first, then runs `golangci-lint`.
- `make test` runs SQLC code generation first, then runs `go test ./...`.
- `make test-fast` skips code generation and runs `go test ./...` directly. Use
  it for faster feedback when you have not changed SQL schema or queries.

## When to run code generation

Run `make codegen` when you change:

- `internal/state/sql/schema.sql`
- `internal/state/sql/queries.sql`

Commit the generated updates under `internal/state/sqlitegen`. CI runs
`git diff --exit-code` after lint and tests, so generated files must be up to
date.

## Golden files

Some CLI help output tests use golden files under `cmd/rascal/testdata/help`.

If you intentionally change CLI help text, refresh those fixtures with:

```bash
UPDATE_GOLDEN=1 go test ./cmd/rascal -run TestHelpGoldenSnapshots
```

Then run `make test` again before submitting.

## Scope and validation

Keep changes focused on the issue you are solving. When behavior changes, update
the relevant tests and any user-facing docs in the same change.
