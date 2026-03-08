package runner

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// NoopLauncher is a safe default for local development.
type NoopLauncher struct{}

func (NoopLauncher) StartDetached(_ context.Context, spec Spec) (handle ExecutionHandle, err error) {
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return ExecutionHandle{}, fmt.Errorf("create run dir: %w", err)
	}
	logPath := filepath.Join(spec.RunDir, "runner.log")
	line := fmt.Sprintf("[%s] noop runner executed for %s\n", time.Now().UTC().Format(time.RFC3339), spec.RunID)
	f, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ExecutionHandle{}, fmt.Errorf("open log file: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close log file: %w", closeErr)
		}
	}()
	if _, err := f.WriteString(line); err != nil {
		return ExecutionHandle{}, fmt.Errorf("write log file: %w", err)
	}

	if err := os.WriteFile(filepath.Join(spec.RunDir, "agent.ndjson"), []byte(`{"event":"noop","run_id":"`+spec.RunID+`"}`+"\n"), 0o644); err != nil {
		return ExecutionHandle{}, fmt.Errorf("write agent log: %w", err)
	}
	meta := Meta{
		RunID:      spec.RunID,
		TaskID:     spec.TaskID,
		Repo:       spec.Repo,
		BaseBranch: spec.BaseBranch,
		HeadBranch: spec.HeadBranch,
		ExitCode:   0,
	}
	if err := WriteMeta(filepath.Join(spec.RunDir, "meta.json"), meta); err != nil {
		return ExecutionHandle{}, err
	}

	return ExecutionHandle{
		Backend: "noop",
		ID:      strings.TrimSpace(spec.RunID),
		Name:    sanitizeContainerName("rascal-" + spec.RunID),
	}, nil
}

func (NoopLauncher) Inspect(_ context.Context, _ ExecutionHandle) (ExecutionState, error) {
	exitCode := 0
	return ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func (NoopLauncher) Stop(_ context.Context, _ ExecutionHandle, _ time.Duration) error {
	return nil
}

func (NoopLauncher) Remove(_ context.Context, _ ExecutionHandle) error {
	return nil
}
