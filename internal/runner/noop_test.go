package runner

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

func TestNoopLauncherWritesArtifacts(t *testing.T) {
	t.Parallel()

	runDir := t.TempDir()
	spec := Spec{
		RunID:      "run_123",
		TaskID:     "task_123",
		Repo:       "owner/repo",
		BaseBranch: "main",
		HeadBranch: "rascal/task/run_123",
		RunDir:     runDir,
	}

	launcher := NoopLauncher{}
	handle, err := launcher.StartDetached(context.Background(), spec)
	if err != nil {
		t.Fatalf("start noop launcher: %v", err)
	}
	if handle.Backend != ExecutionBackendNoop || handle.ID != spec.RunID {
		t.Fatalf("unexpected execution handle: %+v", handle)
	}

	state, err := launcher.Inspect(context.Background(), handle)
	if err != nil {
		t.Fatalf("inspect noop launcher: %v", err)
	}
	if state.Running {
		t.Fatal("expected noop execution to be terminal")
	}
	if state.ExitCode == nil || *state.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %+v", state.ExitCode)
	}

	for _, name := range []string{"runner.log", "agent.ndjson", "meta.json"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
}
