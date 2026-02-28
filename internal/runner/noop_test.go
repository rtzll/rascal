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

	res, err := (NoopLauncher{}).Start(context.Background(), spec)
	if err != nil {
		t.Fatalf("start noop launcher: %v", err)
	}
	if res.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", res.ExitCode)
	}

	for _, name := range []string{"runner.log", "goose.ndjson", "meta.json"} {
		if _, err := os.Stat(filepath.Join(runDir, name)); err != nil {
			t.Fatalf("expected %s: %v", name, err)
		}
	}
}
