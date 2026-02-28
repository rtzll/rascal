# rascal

Rascal is a self-hosted coding-agent orchestrator.

This repository currently contains a simple, idiomatic Go scaffold with:

- `cmd/rascal`: CLI
- `cmd/rascald`: orchestrator API server
- `internal/*`: focused packages for config, state, runner, webhook verification, and logs
- `deploy/*`: initial deployment assets (systemd, Caddy, scripts)

## Quick start

Run server:

```bash
go run ./cmd/rascald
```

Run CLI:

```bash
go run ./cmd/rascal doctor
go run ./cmd/rascal run -R OWNER/REPO -t "your task"
go run ./cmd/rascal ps
```
