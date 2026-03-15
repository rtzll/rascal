package runner

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/agent"
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

func TestDockerLauncherStartDetachedUsesStableNameAndNoRM(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
cmd="${1:-}"
if [ "$cmd" = "run" ]; then
  echo "container-123"
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
	handle, err := launcher.StartDetached(context.Background(), Spec{
		RunID:       "run_detached",
		TaskID:      "task_detached",
		Repo:        "owner/repo",
		Instruction: "detached",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-detached",
		Trigger:     "cli",
		Debug:       true,
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("start detached: %v", err)
	}
	if handle.Backend != ExecutionBackendDocker || handle.ID != "container-123" {
		t.Fatalf("unexpected handle: %+v", handle)
	}
	if handle.Name != "rascal-run_detached" {
		t.Fatalf("unexpected handle name: %s", handle.Name)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker call log: %v", err)
	}
	logText := string(data)
	if strings.Contains(logText, "--rm") {
		t.Fatalf("detached run should not pass --rm: %s", logText)
	}
	for _, want := range []string{
		"run -d --name rascal-run_detached",
		"--label rascal.run_id=run_detached",
		"--label rascal.task_id=task_detached",
		"--label rascal.repo=owner/repo",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in docker call log:\n%s", want, logText)
		}
	}
}

func TestDockerLauncherInspectStopAndRemove(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
cmd="${1:-}"
target="${6:-}"
if [ "$cmd" = "inspect" ]; then
  if [ "$target" = "running-id" ]; then
    echo "true 0"
    exit 0
  fi
  if [ "$target" = "exited-id" ]; then
    echo "false 17"
    exit 0
  fi
  echo "Error: No such object: $target" >&2
  exit 1
fi
if [ "$cmd" = "stop" ]; then
  exit 0
fi
if [ "$cmd" = "rm" ]; then
  exit 0
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}

	oldPath := os.Getenv("PATH")
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+oldPath)

	launcher := DockerLauncher{Image: "rascal-runner:latest"}
	runningState, err := launcher.Inspect(context.Background(), ExecutionHandle{ID: "running-id"})
	if err != nil {
		t.Fatalf("inspect running container: %v", err)
	}
	if !runningState.Running || runningState.ExitCode != nil {
		t.Fatalf("unexpected running state: %+v", runningState)
	}

	exitedState, err := launcher.Inspect(context.Background(), ExecutionHandle{ID: "exited-id"})
	if err != nil {
		t.Fatalf("inspect exited container: %v", err)
	}
	if exitedState.Running || exitedState.ExitCode == nil || *exitedState.ExitCode != 17 {
		t.Fatalf("unexpected exited state: %+v", exitedState)
	}

	if _, err := launcher.Inspect(context.Background(), ExecutionHandle{ID: "missing-id"}); !errors.Is(err, ErrExecutionNotFound) {
		t.Fatalf("expected ErrExecutionNotFound for missing container, got %v", err)
	}

	if err := launcher.Stop(context.Background(), ExecutionHandle{ID: "running-id"}, 3*time.Second); err != nil {
		t.Fatalf("stop running container: %v", err)
	}
	if err := launcher.Remove(context.Background(), ExecutionHandle{ID: "running-id"}); err != nil {
		t.Fatalf("remove running container: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker call log: %v", err)
	}
	logText := string(data)
	for _, want := range []string{
		"inspect --type container --format {{.State.Running}} {{.State.ExitCode}} running-id",
		"stop --time 3 running-id",
		"rm -f running-id",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in docker call log:\n%s", want, logText)
		}
	}
}

func TestDockerLauncherUsesTaskSessionMountWhenResumeEnabled(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
if [ "${1:-}" = "run" ]; then
  echo "container-uses-session"
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := filepath.Join(tmp, "run")
	sessionDir := filepath.Join(tmp, "sessions", "task")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner:latest", GitHubToken: "gh-token"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		AgentRuntime: agent.BackendGoose,
		RunID:        "run_1",
		TaskID:       "owner/repo#1",
		Repo:         "owner/repo",
		Instruction:  "task",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1",
		Trigger:      "pr_comment",
		Debug:        true,
		RunDir:       runDir,
		TaskSession: SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskDir:          sessionDir,
			TaskKey:          "owner-repo-1-abc123",
			RuntimeSessionID: "rascal-owner-repo-1-abc123",
		},
	})
	if err != nil {
		t.Fatalf("launcher start: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker calls log: %v", err)
	}
	call := string(data)
	if !strings.Contains(call, "-e GOOSE_PATH_ROOT=/rascal-goose-session") {
		t.Fatalf("expected persistent goose path root env, got:\n%s", call)
	}
	if !strings.Contains(call, "-e RASCAL_TASK_SESSION_MODE=pr-only") {
		t.Fatalf("expected session mode env, got:\n%s", call)
	}
	if !strings.Contains(call, "-e RASCAL_TASK_SESSION_RESUME=true") {
		t.Fatalf("expected resume env, got:\n%s", call)
	}
	if !strings.Contains(call, sessionDir+":/rascal-goose-session") {
		t.Fatalf("expected task session mount, got:\n%s", call)
	}

	info, err := os.Stat(sessionDir)
	if err != nil {
		t.Fatalf("stat session dir: %v", err)
	}
	if os.Geteuid() == 0 {
		stat, ok := info.Sys().(*syscall.Stat_t)
		if !ok {
			t.Fatalf("session dir stat type %T, want *syscall.Stat_t", info.Sys())
		}
		if int(stat.Uid) != runtimeUID || int(stat.Gid) != runtimeGID {
			t.Fatalf("session dir ownership = %d:%d, want %d:%d", stat.Uid, stat.Gid, runtimeUID, runtimeGID)
		}
	} else if info.Mode().Perm() != 0o777 {
		t.Fatalf("session dir mode = %o, want 777", info.Mode().Perm())
	}
}

func TestDockerLauncherKeepsRunScopedGoosePathWhenSessionResumeDisabled(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
if [ "${1:-}" = "run" ]; then
  echo "container-run-scoped"
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := filepath.Join(tmp, "run")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner:latest"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		AgentRuntime: agent.BackendGoose,
		RunID:        "run_2",
		TaskID:       "owner/repo#2",
		Repo:         "owner/repo",
		Instruction:  "task",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-2",
		Trigger:      "issue_label",
		Debug:        false,
		RunDir:       runDir,
		TaskSession: SessionSpec{
			Mode: agent.SessionModePROnly,
		},
	})
	if err != nil {
		t.Fatalf("launcher start: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker calls log: %v", err)
	}
	call := string(data)
	if !strings.Contains(call, "-e GOOSE_PATH_ROOT=/rascal-meta/goose") {
		t.Fatalf("expected run-scoped goose path root env, got:\n%s", call)
	}
	if strings.Contains(call, ":/rascal-goose-session") {
		t.Fatalf("did not expect persistent session mount when resume disabled, got:\n%s", call)
	}
}

func TestDockerLauncherUsesTaskScopedCodexHomeWhenResumeEnabled(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
if [ "${1:-}" = "run" ]; then
  echo "container-codex-session"
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := filepath.Join(tmp, "run")
	sessionDir := filepath.Join(tmp, "sessions", "task")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner-codex:latest", GitHubToken: "gh-token"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		RunID:        "run_codex_1",
		TaskID:       "owner/repo#1",
		Repo:         "owner/repo",
		Instruction:  "task",
		AgentRuntime: agent.BackendCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1",
		Trigger:      "pr_comment",
		Debug:        true,
		RunDir:       runDir,
		TaskSession: SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskDir:          sessionDir,
			TaskKey:          "owner-repo-1-abc123",
			RuntimeSessionID: "session-123",
		},
	})
	if err != nil {
		t.Fatalf("launcher start: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read docker calls log: %v", err)
	}
	call := string(data)
	if !strings.Contains(call, "-e CODEX_HOME=/rascal-codex-session") {
		t.Fatalf("expected persistent codex home env, got:\n%s", call)
	}
	if !strings.Contains(call, "-e RASCAL_TASK_SESSION_ID=session-123") {
		t.Fatalf("expected task session id env, got:\n%s", call)
	}
	if !strings.Contains(call, sessionDir+":/rascal-codex-session") {
		t.Fatalf("expected task session mount, got:\n%s", call)
	}
	if strings.Contains(call, "-e GOOSE_PROVIDER=") {
		t.Fatalf("did not expect goose env for codex backend, got:\n%s", call)
	}
}

func TestDockerLauncherIncludesNoNewPrivilegesSecurityOpt(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
if [ "${1:-}" = "run" ]; then
  echo "container-security"
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
	_, err := launcher.StartDetached(context.Background(), Spec{
		RunID:       "run_security",
		TaskID:      "task_security",
		Repo:        "owner/repo",
		Instruction: "security",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-security",
		Trigger:     "cli",
		Debug:       true,
		RunDir:      runDir,
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
