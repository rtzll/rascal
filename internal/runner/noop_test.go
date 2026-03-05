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

	meta, err := ReadMeta(filepath.Join(runDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta: %v", err)
	}
	if meta.RunID != spec.RunID {
		t.Fatalf("meta run_id = %q, want %q", meta.RunID, spec.RunID)
	}
	if meta.TaskID != spec.TaskID {
		t.Fatalf("meta task_id = %q, want %q", meta.TaskID, spec.TaskID)
	}
	if meta.Repo != spec.Repo {
		t.Fatalf("meta repo = %q, want %q", meta.Repo, spec.Repo)
	}
	if meta.ExitCode != 0 {
		t.Fatalf("meta exit_code = %d, want 0", meta.ExitCode)
	}
}
