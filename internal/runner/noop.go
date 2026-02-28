package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// NoopLauncher is a safe default for local development.
type NoopLauncher struct{}

func (NoopLauncher) Start(_ context.Context, spec Spec) (Result, error) {
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create run dir: %w", err)
	}
	logPath := filepath.Join(spec.RunDir, "runner.log")
	line := fmt.Sprintf("[%s] noop runner executed for %s\n", time.Now().UTC().Format(time.RFC3339), spec.RunID)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("open log file: %w", err)
	}
	if _, err := f.WriteString(line); err != nil {
		_ = f.Close()
		return Result{}, fmt.Errorf("write log file: %w", err)
	}
	_ = f.Close()

	_ = os.WriteFile(filepath.Join(spec.RunDir, "goose.ndjson"), []byte(`{"event":"noop","run_id":"`+spec.RunID+`"}`+"\n"), 0o644)
	meta := Meta{
		RunID:      spec.RunID,
		TaskID:     spec.TaskID,
		Repo:       spec.Repo,
		BaseBranch: spec.BaseBranch,
		HeadBranch: spec.HeadBranch,
		ExitCode:   0,
	}
	if err := WriteMeta(filepath.Join(spec.RunDir, "meta.json"), meta); err != nil {
		return Result{}, err
	}

	return Result{ExitCode: 0}, nil
}
