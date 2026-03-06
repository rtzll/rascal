package runner

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
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

func TestDockerLauncherUsesTaskSessionMountWhenResumeEnabled(t *testing.T) {
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
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := filepath.Join(tmp, "run")
	sessionDir := filepath.Join(tmp, "sessions", "task")
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		t.Fatalf("create run dir: %v", err)
	}

	launcher := DockerLauncher{Image: "rascal-runner:latest", GitHubToken: "gh-token"}
	_, err := launcher.Start(context.Background(), Spec{
		RunID:               "run_1",
		TaskID:              "owner/repo#1",
		Repo:                "owner/repo",
		Task:                "task",
		BaseBranch:          "main",
		HeadBranch:          "rascal/task-1",
		Trigger:             "pr_comment",
		Debug:               true,
		RunDir:              runDir,
		GooseSessionMode:    GooseSessionModePROnly,
		GooseSessionResume:  true,
		GooseSessionTaskDir: sessionDir,
		GooseSessionTaskKey: "owner-repo-1-abc123",
		GooseSessionName:    "rascal-owner-repo-1-abc123",
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
	if !strings.Contains(call, "-e RASCAL_GOOSE_SESSION_MODE=pr-only") {
		t.Fatalf("expected session mode env, got:\n%s", call)
	}
	if !strings.Contains(call, "-e RASCAL_GOOSE_SESSION_RESUME=true") {
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
	_, err := launcher.Start(context.Background(), Spec{
		RunID:            "run_2",
		TaskID:           "owner/repo#2",
		Repo:             "owner/repo",
		Task:             "task",
		BaseBranch:       "main",
		HeadBranch:       "rascal/task-2",
		Trigger:          "issue_label",
		Debug:            false,
		RunDir:           runDir,
		GooseSessionMode: GooseSessionModePROnly,
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

func TestDockerLauncherBuildRunArgsOpenMode(t *testing.T) {
	t.Parallel()

	launcher := DockerLauncher{
		Image: "rascal-runner:latest",
		Security: SecurityOptions{
			Mode: securityModeOpen,
		},
	}
	args, _, profile, err := launcher.buildRunArgs(testSpec(), "/tmp/workspace", "", "/rascal-meta/goose")
	if err != nil {
		t.Fatalf("build run args: %v", err)
	}
	if profile.mode != securityModeOpen {
		t.Fatalf("mode = %q, want %q", profile.mode, securityModeOpen)
	}
	if !containsArg(args, "run") || !containsArg(args, "--rm") {
		t.Fatalf("expected base docker run args, got: %v", args)
	}
	if !hasArgPair(args, "--security-opt", "no-new-privileges:true") {
		t.Fatalf("expected open mode to preserve no-new-privileges, got: %v", args)
	}
	for _, forbidden := range []string{
		"--cap-drop=ALL",
		"--init",
		"--read-only",
	} {
		if containsArg(args, forbidden) {
			t.Fatalf("open mode should not include %q in args: %v", forbidden, args)
		}
	}
}

func TestDockerLauncherBuildRunArgsBaselineMode(t *testing.T) {
	t.Parallel()

	launcher := DockerLauncher{
		Image: "rascal-runner:latest",
		Security: SecurityOptions{
			Mode:        securityModeBaseline,
			PidsLimit:   1024,
			MemoryLimit: "6g",
			CPULimit:    "3.5",
		},
	}
	args, _, profile, err := launcher.buildRunArgs(testSpec(), "/tmp/workspace", "", "/rascal-meta/goose")
	if err != nil {
		t.Fatalf("build run args: %v", err)
	}
	if profile.mode != securityModeBaseline {
		t.Fatalf("mode = %q, want %q", profile.mode, securityModeBaseline)
	}
	for _, required := range []string{
		"--cap-drop=ALL",
		"--init",
		"--pids-limit=1024",
		"--memory=6g",
		"--cpus=3.5",
	} {
		if !containsArg(args, required) {
			t.Fatalf("baseline mode missing %q in args: %v", required, args)
		}
	}
	if !hasArgPair(args, "--security-opt", "no-new-privileges:true") {
		t.Fatalf("baseline mode missing no-new-privileges security-opt: %v", args)
	}
	if containsArg(args, "--read-only") {
		t.Fatalf("baseline mode should not include strict flags: %v", args)
	}
}

func TestDockerLauncherBuildRunArgsStrictMode(t *testing.T) {
	t.Parallel()

	launcher := DockerLauncher{
		Image: "rascal-runner:latest",
		Security: SecurityOptions{
			Mode:        securityModeStrict,
			PidsLimit:   700,
			MemoryLimit: "5g",
			CPULimit:    "2",
		},
	}
	args, _, profile, err := launcher.buildRunArgs(testSpec(), "/tmp/workspace", "", "/rascal-meta/goose")
	if err != nil {
		t.Fatalf("build run args: %v", err)
	}
	if profile.mode != securityModeStrict {
		t.Fatalf("mode = %q, want %q", profile.mode, securityModeStrict)
	}
	for _, required := range []string{
		"--cap-drop=ALL",
		"--init",
		"--pids-limit=700",
		"--memory=5g",
		"--cpus=2",
		"--read-only",
		"--tmpfs=/tmp:rw,nosuid,nodev,noexec,size=64m",
		"--tmpfs=/var/tmp:rw,nosuid,nodev,noexec,size=64m",
	} {
		if !containsArg(args, required) {
			t.Fatalf("strict mode missing %q in args: %v", required, args)
		}
	}
	if !hasArgPair(args, "--security-opt", "no-new-privileges:true") {
		t.Fatalf("strict mode missing no-new-privileges security-opt: %v", args)
	}
	if !hasArgPair(args, "--security-opt", "seccomp=default") {
		t.Fatalf("strict mode missing seccomp security-opt: %v", args)
	}
}

func TestDockerLauncherBuildRunArgsInvalidMode(t *testing.T) {
	t.Parallel()

	launcher := DockerLauncher{
		Image: "rascal-runner:latest",
		Security: SecurityOptions{
			Mode: "invalid",
		},
	}
	_, _, _, err := launcher.buildRunArgs(testSpec(), "/tmp/workspace", "", "/rascal-meta/goose")
	if err == nil || !strings.Contains(err.Error(), "invalid docker security mode") {
		t.Fatalf("expected invalid security mode error, got %v", err)
	}
}

func TestDockerLauncherBuildRunArgsBaselineDefaults(t *testing.T) {
	t.Parallel()

	launcher := DockerLauncher{
		Image: "rascal-runner:latest",
		Security: SecurityOptions{
			Mode: securityModeBaseline,
		},
	}
	args, _, _, err := launcher.buildRunArgs(testSpec(), "/tmp/workspace", "", "/rascal-meta/goose")
	if err != nil {
		t.Fatalf("build run args: %v", err)
	}
	if !containsArg(args, "--pids-limit="+strconv.Itoa(defaultPidsLimit)) {
		t.Fatalf("expected default pids limit in args: %v", args)
	}
	if !containsArg(args, "--memory=4g") {
		t.Fatalf("expected default memory limit in args: %v", args)
	}
	if !containsArg(args, "--cpus=2") {
		t.Fatalf("expected default cpu limit in args: %v", args)
	}
}

func testSpec() Spec {
	return Spec{
		RunID:      "run_123",
		TaskID:     "task_123",
		Task:       "test task",
		Repo:       "owner/repo",
		BaseBranch: "main",
		HeadBranch: "rascal/test",
		Trigger:    "cli",
		Context:    "{}",
	}
}

func containsArg(args []string, want string) bool {
	for _, arg := range args {
		if arg == want {
			return true
		}
	}
	return false
}

func hasArgPair(args []string, key, value string) bool {
	for i := 0; i+1 < len(args); i++ {
		if args[i] == key && args[i+1] == value {
			return true
		}
	}
	return false
}
