package config

import (
	"path/filepath"
	"testing"
)

func TestLoadServerConfigRunnerEnvPrefersRuntimeFields(t *testing.T) {
	t.Setenv("RASCAL_RUNNER_RUNTIME", "docker")
	t.Setenv("RASCAL_RUNNER_ARTIFACT_REF", "runtime-ref")
	t.Setenv("RASCAL_RUNNER_MODE", "noop")
	t.Setenv("RASCAL_RUNNER_IMAGE", "legacy-ref")

	cfg := LoadServerConfig()
	if cfg.RunnerRuntime != "docker" {
		t.Fatalf("RunnerRuntime = %q, want docker", cfg.RunnerRuntime)
	}
	if cfg.RunnerArtifactRef != "runtime-ref" {
		t.Fatalf("RunnerArtifactRef = %q, want runtime-ref", cfg.RunnerArtifactRef)
	}
}

func TestLoadServerConfigRunnerEnvFallsBackToLegacy(t *testing.T) {
	t.Setenv("RASCAL_RUNNER_RUNTIME", "")
	t.Setenv("RASCAL_RUNNER_ARTIFACT_REF", "")
	t.Setenv("RASCAL_RUNNER_MODE", "docker")
	t.Setenv("RASCAL_RUNNER_IMAGE", "legacy-ref")

	cfg := LoadServerConfig()
	if cfg.RunnerRuntime != "docker" {
		t.Fatalf("RunnerRuntime = %q, want docker", cfg.RunnerRuntime)
	}
	if cfg.RunnerArtifactRef != "legacy-ref" {
		t.Fatalf("RunnerArtifactRef = %q, want legacy-ref", cfg.RunnerArtifactRef)
	}
}

func TestLoadServerConfigRunnerDefaults(t *testing.T) {
	t.Setenv("RASCAL_RUNNER_RUNTIME", "")
	t.Setenv("RASCAL_RUNNER_ARTIFACT_REF", "")
	t.Setenv("RASCAL_RUNNER_MODE", "")
	t.Setenv("RASCAL_RUNNER_IMAGE", "")

	cfg := LoadServerConfig()
	if cfg.RunnerRuntime != "noop" {
		t.Fatalf("RunnerRuntime = %q, want noop", cfg.RunnerRuntime)
	}
	if cfg.RunnerArtifactRef != "rascal-runner:latest" {
		t.Fatalf("RunnerArtifactRef = %q, want rascal-runner:latest", cfg.RunnerArtifactRef)
	}
}

func TestLoadServerConfigGooseSessionDefaults(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "rascal-data")
	t.Setenv("RASCAL_DATA_DIR", dataDir)
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_GOOSE_SESSION_ROOT", "")
	t.Setenv("RASCAL_GOOSE_SESSION_TTL_DAYS", "")

	cfg := LoadServerConfig()
	if cfg.GooseSessionMode != "all" {
		t.Fatalf("GooseSessionMode = %q, want all", cfg.GooseSessionMode)
	}
	wantRoot := filepath.Join(dataDir, "goose-sessions")
	if cfg.GooseSessionRoot != wantRoot {
		t.Fatalf("GooseSessionRoot = %q, want %q", cfg.GooseSessionRoot, wantRoot)
	}
	if cfg.GooseSessionTTLDays != 14 {
		t.Fatalf("GooseSessionTTLDays = %d, want 14", cfg.GooseSessionTTLDays)
	}
}

func TestLoadServerConfigGooseSessionOverrides(t *testing.T) {
	root := filepath.Join(t.TempDir(), "goose-root")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "PR-ONLY")
	t.Setenv("RASCAL_GOOSE_SESSION_ROOT", root)
	t.Setenv("RASCAL_GOOSE_SESSION_TTL_DAYS", "0")

	cfg := LoadServerConfig()
	if cfg.GooseSessionMode != "pr-only" {
		t.Fatalf("GooseSessionMode = %q, want pr-only", cfg.GooseSessionMode)
	}
	if cfg.GooseSessionRoot != root {
		t.Fatalf("GooseSessionRoot = %q, want %q", cfg.GooseSessionRoot, root)
	}
	if cfg.GooseSessionTTLDays != 0 {
		t.Fatalf("GooseSessionTTLDays = %d, want 0", cfg.GooseSessionTTLDays)
	}
}
