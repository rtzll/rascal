package runner

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
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

func TestDockerContainerEnvBuilderBuildGooseCodex(t *testing.T) {
	t.Parallel()

	spec := Spec{
		RunID:        "run_1",
		TaskID:       "owner/repo#1",
		Repo:         "owner/repo",
		Instruction:  "task",
		AgentRuntime: runtime.RuntimeGooseCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1",
		Trigger:      runtrigger.NameIssueLabel,
		Debug:        true,
		IssueNumber:  19,
		PRNumber:     0,
		Context:      "repo context",
		TaskSession: TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskDir:          "/tmp/session",
			TaskKey:          "owner-repo-1-abc123",
			RuntimeSessionID: "session-123",
		},
	}

	layout := newDockerRuntimeLayout(runtime.RuntimeGooseCodex, spec.TaskSession)
	got := newDockerContainerEnvBuilder(spec, runtime.RuntimeGooseCodex, layout, "gh-token", false).Build()
	want := map[string]string{
		"CODEX_HOME":                   containerCodexStateDir,
		"CODEX_AUTH_FILE":              containerCodexAuthFile,
		"GH_PROMPT_DISABLED":           "1",
		"GH_TOKEN_FILE":                containerGitHubTokenFile,
		"GIT_TERMINAL_PROMPT":          "0",
		"GOOSE_CONTEXT_STRATEGY":       "summarize",
		"GOOSE_DISABLE_KEYRING":        "1",
		"GOOSE_DISABLE_SESSION_NAMING": "true",
		"GOOSE_MODE":                   "auto",
		"GOOSE_MODEL":                  "gpt-5.4",
		"GOOSE_MOIM_MESSAGE_FILE":      containerGooseMOIMPath,
		"GOOSE_PATH_ROOT":              containerGooseSessionDir,
		"GOOSE_PROVIDER":               "codex",
		"RASCAL_AGENT_RUNTIME":         runtime.RuntimeGooseCodex.String(),
		"RASCAL_BASE_BRANCH":           "main",
		"RASCAL_CONTEXT":               "repo context",
		"RASCAL_CONTEXT_JSON":          containerContextJSONPath,
		"RASCAL_GOOSE_DEBUG":           "true",
		"RASCAL_HEAD_BRANCH":           "rascal/task-1",
		"RASCAL_INSTRUCTION":           "task",
		"RASCAL_ISSUE_NUMBER":          "19",
		"RASCAL_PR_NUMBER":             "0",
		"RASCAL_REPO":                  "owner/repo",
		"RASCAL_RUN_ID":                "run_1",
		"RASCAL_TASK_ID":               "owner/repo#1",
		"RASCAL_TASK_SESSION_ID":       "session-123",
		"RASCAL_TASK_SESSION_KEY":      "owner-repo-1-abc123",
		"RASCAL_TASK_SESSION_MODE":     "pr-only",
		"RASCAL_TASK_SESSION_RESUME":   "true",
		"RASCAL_TRIGGER":               runtrigger.NameIssueLabel.String(),
	}
	if !maps.Equal(got, want) {
		t.Fatalf("unexpected env map (-got +want):\n got: %#v\nwant: %#v", got, want)
	}
}

func TestDockerContainerEnvBuilderBuildDirectRuntimes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		runtime runtime.Runtime
		session TaskSessionSpec
		want    map[string]string
	}{
		{
			name:    "codex resume",
			runtime: runtime.RuntimeCodex,
			session: TaskSessionSpec{
				Mode:             runtime.SessionModeAll,
				Resume:           true,
				TaskDir:          "/tmp/codex-session",
				TaskKey:          "task-key",
				RuntimeSessionID: "codex-session-id",
			},
			want: map[string]string{
				"CODEX_AUTH_FILE":            containerCodexAuthFile,
				"CODEX_HOME":                 containerCodexSessionDir,
				"GH_PROMPT_DISABLED":         "1",
				"GIT_TERMINAL_PROMPT":        "0",
				"RASCAL_AGENT_RUNTIME":       runtime.RuntimeCodex.String(),
				"RASCAL_BASE_BRANCH":         "main",
				"RASCAL_CONTEXT":             "",
				"RASCAL_CONTEXT_JSON":        containerContextJSONPath,
				"RASCAL_GOOSE_DEBUG":         "false",
				"RASCAL_HEAD_BRANCH":         "rascal/task-2",
				"RASCAL_INSTRUCTION":         "task",
				"RASCAL_ISSUE_NUMBER":        "0",
				"RASCAL_PR_NUMBER":           "24",
				"RASCAL_REPO":                "owner/repo",
				"RASCAL_RUN_ID":              "run_2",
				"RASCAL_TASK_ID":             "owner/repo#2",
				"RASCAL_TASK_SESSION_ID":     "codex-session-id",
				"RASCAL_TASK_SESSION_KEY":    "task-key",
				"RASCAL_TASK_SESSION_MODE":   "all",
				"RASCAL_TASK_SESSION_RESUME": "true",
				"RASCAL_TRIGGER":             runtrigger.NamePRComment.String(),
			},
		},
		{
			name:    "claude resume",
			runtime: runtime.RuntimeClaude,
			session: TaskSessionSpec{
				Mode:             runtime.SessionModePROnly,
				Resume:           true,
				TaskDir:          "/tmp/claude-session",
				TaskKey:          "task-key-2",
				RuntimeSessionID: "claude-session-id",
			},
			want: map[string]string{
				"CLAUDE_CODE_OAUTH_TOKEN_FILE": containerClaudeTokenFile,
				"CLAUDE_CONFIG_DIR":            containerClaudeSessionDir,
				"CODEX_HOME":                   containerCodexStateDir,
				"GH_PROMPT_DISABLED":           "1",
				"GIT_TERMINAL_PROMPT":          "0",
				"RASCAL_AGENT_RUNTIME":         runtime.RuntimeClaude.String(),
				"RASCAL_BASE_BRANCH":           "main",
				"RASCAL_CONTEXT":               "",
				"RASCAL_CONTEXT_JSON":          containerContextJSONPath,
				"RASCAL_GOOSE_DEBUG":           "false",
				"RASCAL_HEAD_BRANCH":           "rascal/task-2",
				"RASCAL_INSTRUCTION":           "task",
				"RASCAL_ISSUE_NUMBER":          "0",
				"RASCAL_PR_NUMBER":             "24",
				"RASCAL_REPO":                  "owner/repo",
				"RASCAL_RUN_ID":                "run_2",
				"RASCAL_TASK_ID":               "owner/repo#2",
				"RASCAL_TASK_SESSION_ID":       "claude-session-id",
				"RASCAL_TASK_SESSION_KEY":      "task-key-2",
				"RASCAL_TASK_SESSION_MODE":     "pr-only",
				"RASCAL_TASK_SESSION_RESUME":   "true",
				"RASCAL_TRIGGER":               runtrigger.NamePRComment.String(),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			spec := Spec{
				RunID:        "run_2",
				TaskID:       "owner/repo#2",
				Repo:         "owner/repo",
				Instruction:  "task",
				AgentRuntime: tt.runtime,
				BaseBranch:   "main",
				HeadBranch:   "rascal/task-2",
				Trigger:      runtrigger.NamePRComment,
				Debug:        false,
				PRNumber:     24,
				TaskSession:  tt.session,
			}
			layout := newDockerRuntimeLayout(tt.runtime, tt.session)
			got := newDockerContainerEnvBuilder(spec, tt.runtime, layout, "", false).Build()
			if !maps.Equal(got, tt.want) {
				t.Fatalf("unexpected env map (-got +want):\n got: %#v\nwant: %#v", got, tt.want)
			}
		})
	}
}

func TestDockerContainerEnvBuilderAllowsLegacyEnvSecrets(t *testing.T) {
	t.Parallel()

	spec := Spec{
		RunID:        "run_env_secret",
		TaskID:       "owner/repo#3",
		Repo:         "owner/repo",
		Instruction:  "task",
		AgentRuntime: runtime.RuntimeCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-3",
		Trigger:      runtrigger.NameCLI,
	}

	layout := newDockerRuntimeLayout(runtime.RuntimeCodex, spec.TaskSession)
	got := newDockerContainerEnvBuilder(spec, runtime.RuntimeCodex, layout, "gh-token", true).Build()
	if got["GH_TOKEN"] != "gh-token" {
		t.Fatalf("GH_TOKEN = %q, want gh-token", got["GH_TOKEN"])
	}
	if _, ok := got["GH_TOKEN_FILE"]; ok {
		t.Fatalf("did not expect GH_TOKEN_FILE when env secrets are enabled: %#v", got)
	}
	if got["CODEX_AUTH_FILE"] != containerCodexAuthFile {
		t.Fatalf("CODEX_AUTH_FILE = %q, want %q", got["CODEX_AUTH_FILE"], containerCodexAuthFile)
	}
}

func TestDockerRunnerStartDetachedUsesStableNameAndNoRM(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner:latest"}
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

func TestDockerRunnerInspectStopAndRemove(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner:latest"}
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

func TestDockerRunnerUsesTaskSessionMountWhenResumeEnabled(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner:latest", GitHubToken: "gh-token"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		AgentRuntime: runtime.RuntimeGooseCodex,
		RunID:        "run_1",
		TaskID:       "owner/repo#1",
		Repo:         "owner/repo",
		Instruction:  "task",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1",
		Trigger:      "pr_comment",
		Debug:        true,
		RunDir:       runDir,
		TaskSession: TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
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
	if !strings.Contains(call, "-e GOOSE_MOIM_MESSAGE_FILE=/rascal-meta/persistent_instructions.md") {
		t.Fatalf("expected goose persistent instructions env, got:\n%s", call)
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

func TestDockerRunnerKeepsRunScopedGoosePathWhenSessionResumeDisabled(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner:latest"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		AgentRuntime: runtime.RuntimeGooseCodex,
		RunID:        "run_2",
		TaskID:       "owner/repo#2",
		Repo:         "owner/repo",
		Instruction:  "task",
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-2",
		Trigger:      "issue_label",
		Debug:        false,
		RunDir:       runDir,
		TaskSession: TaskSessionSpec{
			Mode: runtime.SessionModePROnly,
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
	if !strings.Contains(call, "-e GOOSE_MOIM_MESSAGE_FILE=/rascal-meta/persistent_instructions.md") {
		t.Fatalf("expected goose persistent instructions env, got:\n%s", call)
	}
	if strings.Contains(call, ":/rascal-goose-session") {
		t.Fatalf("did not expect persistent session mount when resume disabled, got:\n%s", call)
	}
}

func TestDockerRunnerUsesTaskScopedCodexHomeWhenResumeEnabled(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner-codex:latest", GitHubToken: "gh-token"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		RunID:        "run_codex_1",
		TaskID:       "owner/repo#1",
		Repo:         "owner/repo",
		Instruction:  "task",
		AgentRuntime: runtime.RuntimeCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/task-1",
		Trigger:      "pr_comment",
		Debug:        true,
		RunDir:       runDir,
		TaskSession: TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
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
	if strings.Contains(call, "-e GOOSE_MOIM_MESSAGE_FILE=") {
		t.Fatalf("did not expect goose persistent instructions env for codex backend, got:\n%s", call)
	}
	if strings.Contains(call, "-e GOOSE_PROVIDER=") {
		t.Fatalf("did not expect goose env for codex backend, got:\n%s", call)
	}
}

func TestDockerRunnerIncludesNoNewPrivilegesSecurityOpt(t *testing.T) {
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

	launcher := DockerRunner{Image: "rascal-runner:latest"}
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

func TestDockerSecurityConfigDockerRunArgsByMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		cfg  DockerSecurityConfig
		want []string
	}{
		{
			name: "open",
			cfg: DockerSecurityConfig{
				Mode: DockerSecurityOpen,
			},
			want: []string{"--security-opt", "no-new-privileges:true"},
		},
		{
			name: "baseline",
			cfg: DockerSecurityConfig{
				Mode:      DockerSecurityBaseline,
				CPUs:      "2",
				Memory:    "4g",
				PidsLimit: 256,
			},
			want: []string{
				"--security-opt", "no-new-privileges:true",
				"--cap-drop", "ALL",
				"--init",
				"--cpus", "2",
				"--memory", "4g",
				"--pids-limit", "256",
			},
		},
		{
			name: "strict",
			cfg: DockerSecurityConfig{
				Mode:         DockerSecurityStrict,
				CPUs:         "2",
				Memory:       "4g",
				PidsLimit:    256,
				TmpfsTmpSize: "512m",
			},
			want: []string{
				"--security-opt", "no-new-privileges:true",
				"--cap-drop", "ALL",
				"--init",
				"--cpus", "2",
				"--memory", "4g",
				"--pids-limit", "256",
				"--tmpfs", "/tmp:rw,nosuid,size=512m",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := tt.cfg.dockerRunArgs()
			if !slices.Equal(got, tt.want) {
				t.Fatalf("dockerRunArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestDockerRunnerLogsSecuritySummary(t *testing.T) {
	tmp := t.TempDir()
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
if [ "${1:-}" = "run" ]; then
  echo "container-security-log"
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

	launcher := DockerRunner{
		Image: "rascal-runner:latest",
		Security: DockerSecurityConfig{
			Mode:      DockerSecurityBaseline,
			CPUs:      "2",
			Memory:    "4g",
			PidsLimit: 256,
		},
	}
	_, err := launcher.StartDetached(context.Background(), Spec{
		RunID:       "run_security_log",
		TaskID:      "task_security_log",
		Repo:        "owner/repo",
		Instruction: "security log",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-security-log",
		Trigger:     "cli",
		Debug:       true,
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "runner.log"))
	if err != nil {
		t.Fatalf("read runner log: %v", err)
	}
	logText := string(data)
	for _, want := range []string{
		"docker security mode=baseline",
		"env_secrets=false",
		"cpus=2",
		"memory=4g",
		"pids=256",
	} {
		if !strings.Contains(logText, want) {
			t.Fatalf("expected %q in runner log:\n%s", want, logText)
		}
	}
}

func TestDockerRunnerMountsSecretsReadOnlyByDefault(t *testing.T) {
	tmp := t.TempDir()
	logPath := filepath.Join(tmp, "docker_calls.log")
	fakeDocker := filepath.Join(tmp, "docker")
	script := `#!/bin/sh
set -eu
echo "$@" >> "` + logPath + `"
if [ "${1:-}" = "run" ]; then
  echo "container-secrets"
fi
exit 0
`
	if err := os.WriteFile(fakeDocker, []byte(script), 0o755); err != nil {
		t.Fatalf("write fake docker: %v", err)
	}
	t.Setenv("PATH", tmp+string(os.PathListSeparator)+os.Getenv("PATH"))

	runDir := filepath.Join(tmp, "run")
	secretsDir := filepath.Join(tmp, ".run-secrets")
	if err := os.MkdirAll(filepath.Join(secretsDir), 0o700); err != nil {
		t.Fatalf("create secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "codex_auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}

	launcher := DockerRunner{Image: "rascal-runner:latest", GitHubToken: "gh-token"}
	_, err := launcher.StartDetached(context.Background(), Spec{
		RunID:       "run_secrets",
		TaskID:      "task_secrets",
		Repo:        "owner/repo",
		Instruction: "secrets",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-secrets",
		Trigger:     "cli",
		Debug:       true,
		RunDir:      runDir,
		SecretsDir:  secretsDir,
	})
	if err != nil {
		t.Fatalf("unexpected start error: %v", err)
	}

	logData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake docker log: %v", err)
	}
	call := string(logData)
	if strings.Contains(call, "-e GH_TOKEN=gh-token") {
		t.Fatalf("did not expect raw GH_TOKEN env in docker args:\n%s", call)
	}
	for _, want := range []string{
		"-e GH_TOKEN_FILE=/run/rascal-secrets/gh_token",
		"-e CODEX_AUTH_FILE=/run/rascal-secrets/codex_auth.json",
		secretsDir + ":/run/rascal-secrets:ro",
	} {
		if !strings.Contains(call, want) {
			t.Fatalf("expected %q in docker args:\n%s", want, call)
		}
	}
}

func TestPrepareMountAccessMakesSecretsReadableForNonRoot(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("non-root readability path only")
	}

	tmp := t.TempDir()
	runDir := filepath.Join(tmp, "run")
	workspaceDir := filepath.Join(runDir, "workspace")
	secretsDir := filepath.Join(tmp, ".run-secrets")

	for _, dir := range []string{runDir, workspaceDir, secretsDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	for _, file := range []string{
		filepath.Join(secretsDir, "gh_token"),
		filepath.Join(secretsDir, "codex_auth.json"),
		filepath.Join(secretsDir, "claude_oauth_token"),
	} {
		if err := os.WriteFile(file, []byte("secret"), 0o600); err != nil {
			t.Fatalf("write %s: %v", file, err)
		}
	}

	if err := prepareMountAccess(runDir, workspaceDir, "", secretsDir); err != nil {
		t.Fatalf("prepareMountAccess: %v", err)
	}

	for _, path := range []string{
		secretsDir,
		filepath.Join(secretsDir, "gh_token"),
		filepath.Join(secretsDir, "codex_auth.json"),
		filepath.Join(secretsDir, "claude_oauth_token"),
	} {
		info, err := os.Stat(path)
		if err != nil {
			t.Fatalf("stat %s: %v", path, err)
		}
		got := info.Mode().Perm()
		want := os.FileMode(0o644)
		if path == secretsDir {
			want = 0o777
		}
		if got != want {
			t.Fatalf("%s mode = %#o, want %#o", path, got, want)
		}
	}
}
