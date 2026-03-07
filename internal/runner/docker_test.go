package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestSanitizeContainerName(t *testing.T) {
	t.Parallel()

	got := sanitizeContainerName("Rascal/Run:ABC 123")
	if got != "rascal-run-abc-123" {
		t.Fatalf("unexpected sanitized name: %q", got)
	}

	longIn := strings.Repeat("x", 80)
	longOut := sanitizeContainerName(longIn)
	if len(longOut) > 63 {
		t.Fatalf("expected max 63 chars, got %d", len(longOut))
	}
}

func TestDockerLauncherStopsContainerOnCancel(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	stopFile := filepath.Join(tmp, "stop.flag")
	stopCalled := filepath.Join(tmp, "stop.called")
	rmCalled := filepath.Join(tmp, "rm.called")

	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
cmd="${1:-}"
if [ "$cmd" = "run" ]; then
  while [ ! -f "` + stopFile + `" ]; do
    sleep 0.05
  done
  exit 143
fi
if [ "$cmd" = "stop" ]; then
  : > "` + stopCalled + `"
  : > "` + stopFile + `"
  exit 0
fi
if [ "$cmd" = "rm" ]; then
  : > "` + rmCalled + `"
  exit 0
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	runDir := filepath.Join(tmp, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner:latest"}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := launcher.Start(ctx, Spec{
			RunID:      "run_cancel",
			TaskID:     "task_cancel",
			Repo:       "owner/repo",
			Task:       "cancel",
			BaseBranch: "main",
			HeadBranch: "rascal/task-cancel",
			Trigger:    "cli",
			Debug:      true,
			RunDir:     runDir,
		})
		done <- err
	}()

	time.Sleep(150 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "context canceled") {
			t.Fatalf("expected context canceled error, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("timeout waiting for launcher to stop after cancel")
	}

	waitForFile := func(path string) error {
		deadline := time.Now().Add(2 * time.Second)
		for time.Now().Before(deadline) {
			if _, err := os.Stat(path); err == nil {
				return nil
			}
			time.Sleep(20 * time.Millisecond)
		}
		_, err := os.Stat(path)
		return err
	}

	if err := waitForFile(stopCalled); err != nil {
		t.Fatalf("expected docker stop to be called: %v", err)
	}
	if err := waitForFile(rmCalled); err != nil {
		t.Fatalf("expected docker rm to be called: %v", err)
	}
}

func TestDockerLauncherIncludesNoNewPrivilegesSecurityOpt(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	runDir := filepath.Join(tmp, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner:latest"}
	_, err := launcher.Start(context.Background(), Spec{
		RunID:      "run_security",
		TaskID:     "task_security",
		Repo:       "owner/repo",
		Task:       "security",
		BaseBranch: "main",
		HeadBranch: "rascal/task-security",
		Trigger:    "cli",
		Debug:      true,
		RunDir:     runDir,
	})
	if err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker log: %v", err)
	}
	fields := strings.Fields(string(logData))
	securityOptIdx := slices.Index(fields, "--security-opt")
	if securityOptIdx == -1 {
		t.Fatalf("expected --security-opt in docker args, got: %s", string(logData))
	}
	if securityOptIdx+1 >= len(fields) || fields[securityOptIdx+1] != "no-new-privileges:true" {
		t.Fatalf("expected no-new-privileges:true in docker args, got: %s", string(logData))
	}
}
