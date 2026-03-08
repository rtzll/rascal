package runner

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/rtzll/rascal/internal/agent"
)

// DockerLauncher runs a task inside a Docker container.
type DockerLauncher struct {
	DefaultImage string
	Image        string
	GitHubToken  string
}

const (
	// Keep in sync with runner/Dockerfile runtime user UID/GID.
	runtimeUID = 10001
	runtimeGID = 10001

	containerMetaDir         = "/rascal-meta"
	containerWorkDir         = "/work"
	containerGooseStateDir   = "/rascal-meta/goose"
	containerCodexStateDir   = "/rascal-meta/codex"
	containerGooseSessionDir = "/rascal-goose-session"
	containerCodexSessionDir = "/rascal-codex-session"
	containerContextJSONPath = "/rascal-meta/context.json"
)

func (l DockerLauncher) StartDetached(ctx context.Context, spec Spec) (handle ExecutionHandle, err error) {
	backend := agent.NormalizeBackend(string(spec.AgentBackend))
	image := strings.TrimSpace(spec.RunnerImage)
	if image == "" {
		image = strings.TrimSpace(l.DefaultImage)
	}
	if image == "" {
		image = strings.TrimSpace(l.Image)
	}
	if image == "" {
		return ExecutionHandle{}, fmt.Errorf("docker image is required")
	}
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return ExecutionHandle{}, fmt.Errorf("create run dir: %w", err)
	}
	workspaceDir := filepath.Join(spec.RunDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return ExecutionHandle{}, fmt.Errorf("create workspace dir: %w", err)
	}
	sessionDir := strings.TrimSpace(spec.AgentSession.TaskDir)
	if sessionDir == "" {
		sessionDir = strings.TrimSpace(spec.GooseSessionTaskDir)
	}
	sessionResume := spec.AgentSession.Resume || spec.GooseSessionResume
	sessionMode := spec.AgentSession.Mode
	if sessionMode == "" {
		sessionMode = agent.NormalizeSessionMode(spec.GooseSessionMode)
	}
	sessionKey := strings.TrimSpace(spec.AgentSession.TaskKey)
	if sessionKey == "" {
		sessionKey = strings.TrimSpace(spec.GooseSessionTaskKey)
	}
	backendSessionID := strings.TrimSpace(spec.AgentSession.BackendSessionID)
	if backendSessionID == "" {
		backendSessionID = strings.TrimSpace(spec.GooseSessionName)
	}
	if sessionResume && sessionDir != "" {
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return ExecutionHandle{}, fmt.Errorf("create agent session dir: %w", err)
		}
	}
	if err := prepareMountAccess(spec.RunDir, workspaceDir, sessionDir); err != nil {
		return ExecutionHandle{}, err
	}

	logPath := filepath.Join(spec.RunDir, "runner.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return ExecutionHandle{}, fmt.Errorf("open runner log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close runner log: %w", closeErr)
		}
	}()

	if _, err := fmt.Fprintf(logFile, "[%s] starting docker runner image=%s backend=%s run_id=%s\n", time.Now().UTC().Format(time.RFC3339), image, backend, spec.RunID); err != nil {
		return ExecutionHandle{}, fmt.Errorf("write runner log header: %w", err)
	}

	codexHome := containerCodexStateDir
	goosePathRoot := containerGooseStateDir
	sessionMountTarget := ""
	if sessionResume && sessionDir != "" {
		switch backend {
		case agent.BackendCodex:
			codexHome = containerCodexSessionDir
			sessionMountTarget = containerCodexSessionDir
		default:
			goosePathRoot = containerGooseSessionDir
			sessionMountTarget = containerGooseSessionDir
		}
	}
	envPairs := map[string]string{
		"RASCAL_RUN_ID":               spec.RunID,
		"RASCAL_TASK_ID":              spec.TaskID,
		"RASCAL_TASK":                 spec.Task,
		"RASCAL_REPO":                 spec.Repo,
		"RASCAL_AGENT_BACKEND":        backend.String(),
		"RASCAL_BASE_BRANCH":          spec.BaseBranch,
		"RASCAL_HEAD_BRANCH":          spec.HeadBranch,
		"RASCAL_TRIGGER":              spec.Trigger,
		"RASCAL_GOOSE_DEBUG":          strconv.FormatBool(spec.Debug),
		"RASCAL_CONTEXT":              spec.Context,
		"RASCAL_CONTEXT_JSON":         containerContextJSONPath,
		"RASCAL_ISSUE_NUMBER":         strconv.Itoa(spec.IssueNumber),
		"RASCAL_PR_NUMBER":            strconv.Itoa(spec.PRNumber),
		"RASCAL_AGENT_SESSION_MODE":   string(sessionMode),
		"RASCAL_AGENT_SESSION_RESUME": strconv.FormatBool(sessionResume),
		"RASCAL_AGENT_SESSION_KEY":    sessionKey,
		"RASCAL_AGENT_SESSION_ID":     backendSessionID,
		"CODEX_HOME":                  codexHome,
		"GH_PROMPT_DISABLED":          "1",
		"GIT_TERMINAL_PROMPT":         "0",
	}
	if backend == agent.BackendGoose {
		envPairs["GOOSE_PATH_ROOT"] = goosePathRoot
		envPairs["GOOSE_PROVIDER"] = "codex"
		envPairs["GOOSE_MODEL"] = "gpt-5.4"
		envPairs["GOOSE_MODE"] = "auto"
		envPairs["GOOSE_DISABLE_KEYRING"] = "1"
		envPairs["GOOSE_DISABLE_SESSION_NAMING"] = "true"
		envPairs["GOOSE_CONTEXT_STRATEGY"] = "summarize"
		envPairs["RASCAL_GOOSE_SESSION_MODE"] = NormalizeGooseSessionMode(string(sessionMode))
		envPairs["RASCAL_GOOSE_SESSION_RESUME"] = strconv.FormatBool(sessionResume)
		envPairs["RASCAL_GOOSE_SESSION_KEY"] = sessionKey
		envPairs["RASCAL_GOOSE_SESSION_NAME"] = backendSessionID
	}
	if strings.TrimSpace(l.GitHubToken) != "" {
		envPairs["GH_TOKEN"] = l.GitHubToken
	}
	containerName := sanitizeContainerName("rascal-" + spec.RunID)
	args := []string{
		"run",
		"-d",
		"--name", containerName,
		"--label", fmt.Sprintf("rascal.run_id=%s", spec.RunID),
		"--label", fmt.Sprintf("rascal.task_id=%s", spec.TaskID),
		"--label", fmt.Sprintf("rascal.repo=%s", spec.Repo),
	}
	envKeys := make([]string, 0, len(envPairs))
	for k := range envPairs {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, envPairs[k]))
	}
	args = append(args,
		"--security-opt", "no-new-privileges:true",
		"-v", fmt.Sprintf("%s:%s", spec.RunDir, containerMetaDir),
		"-v", fmt.Sprintf("%s:%s", workspaceDir, containerWorkDir),
	)
	if sessionMountTarget != "" {
		args = append(args, "-v", fmt.Sprintf("%s:%s", sessionDir, sessionMountTarget))
	}
	args = append(args, image)

	if _, err := fmt.Fprintf(logFile, "[%s] agent session backend=%s mode=%s resume=%t key=%s session_id=%s path_root=%s\n",
		time.Now().UTC().Format(time.RFC3339),
		backend,
		agent.NormalizeSessionMode(string(sessionMode)),
		sessionResume,
		sessionKey,
		backendSessionID,
		firstNonEmptySessionPath(sessionMountTarget, goosePathRoot),
	); err != nil {
		return ExecutionHandle{}, fmt.Errorf("write runner session log: %w", err)
	}

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = logFile
	out, err := cmd.Output()
	if err != nil {
		if errors.Is(ctx.Err(), context.Canceled) {
			return ExecutionHandle{}, context.Canceled
		}
		return ExecutionHandle{}, fmt.Errorf("start detached docker runner: %w", unwrapSyscallError(err))
	}

	containerID := strings.TrimSpace(string(out))
	if containerID == "" {
		return ExecutionHandle{}, fmt.Errorf("docker run -d returned empty container id")
	}
	if _, err := fmt.Fprintf(logFile, "[%s] detached container started name=%s id=%s\n", time.Now().UTC().Format(time.RFC3339), containerName, containerID); err != nil {
		return ExecutionHandle{}, fmt.Errorf("write runner start log: %w", err)
	}
	return ExecutionHandle{
		Backend: "docker",
		ID:      containerID,
		Name:    containerName,
	}, nil
}

func (l DockerLauncher) Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error) {
	target := dockerExecutionTarget(handle)
	if target == "" {
		return ExecutionState{}, fmt.Errorf("execution target is required")
	}
	cmd := exec.CommandContext(ctx, "docker", "inspect", "--type", "container", "--format", "{{.State.Running}} {{.State.ExitCode}}", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if dockerNotFoundOutput(err, out) {
			return ExecutionState{}, ErrExecutionNotFound
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return ExecutionState{}, context.Canceled
		}
		return ExecutionState{}, fmt.Errorf("inspect docker container %s: %w", target, unwrapSyscallError(err))
	}
	parts := strings.Fields(strings.TrimSpace(string(out)))
	if len(parts) < 2 {
		return ExecutionState{}, fmt.Errorf("unexpected docker inspect output for %s: %q", target, strings.TrimSpace(string(out)))
	}
	running := parts[0] == "true"
	if running {
		return ExecutionState{Running: true}, nil
	}
	exitCode, convErr := strconv.Atoi(parts[1])
	if convErr != nil {
		return ExecutionState{}, fmt.Errorf("parse docker exit code %q: %w", parts[1], convErr)
	}
	return ExecutionState{Running: false, ExitCode: &exitCode}, nil
}

func firstNonEmptySessionPath(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func (l DockerLauncher) Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error {
	target := dockerExecutionTarget(handle)
	if target == "" {
		return fmt.Errorf("execution target is required")
	}
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	stopSeconds := int(timeout.Round(time.Second) / time.Second)
	if stopSeconds < 1 {
		stopSeconds = 1
	}
	cmd := exec.CommandContext(ctx, "docker", "stop", "--time", strconv.Itoa(stopSeconds), target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if dockerNotFoundOutput(err, out) {
			return ErrExecutionNotFound
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("stop docker container %s: %w", target, unwrapSyscallError(err))
	}
	return nil
}

func (l DockerLauncher) Remove(ctx context.Context, handle ExecutionHandle) error {
	target := dockerExecutionTarget(handle)
	if target == "" {
		return fmt.Errorf("execution target is required")
	}
	cmd := exec.CommandContext(ctx, "docker", "rm", "-f", target)
	out, err := cmd.CombinedOutput()
	if err != nil {
		if dockerNotFoundOutput(err, out) {
			return nil
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			return context.Canceled
		}
		return fmt.Errorf("remove docker container %s: %w", target, unwrapSyscallError(err))
	}
	return nil
}

func dockerExecutionTarget(handle ExecutionHandle) string {
	if strings.TrimSpace(handle.ID) != "" {
		return strings.TrimSpace(handle.ID)
	}
	return strings.TrimSpace(handle.Name)
}

func dockerNotFoundOutput(err error, output []byte) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(strings.TrimSpace(string(output)))
	return strings.Contains(text, "no such object") || strings.Contains(text, "no such container")
}

func prepareMountAccess(runDir, workspaceDir, sessionDir string) error {
	if os.Geteuid() == 0 {
		if err := chownTree(runDir, runtimeUID, runtimeGID); err != nil {
			return fmt.Errorf("prepare run dir ownership: %w", err)
		}
		if strings.TrimSpace(sessionDir) != "" {
			if err := chownTree(sessionDir, runtimeUID, runtimeGID); err != nil {
				return fmt.Errorf("prepare goose session dir ownership: %w", err)
			}
		}
		return nil
	}

	// Non-root launcher fallback: make bind mounts writable by the runtime UID.
	targets := []string{runDir, workspaceDir, filepath.Join(runDir, "codex"), filepath.Join(runDir, "goose")}
	if strings.TrimSpace(sessionDir) != "" {
		targets = append(targets, sessionDir)
	}
	for _, target := range targets {
		if err := chmodIfExists(target, 0o777); err != nil {
			return fmt.Errorf("prepare writable mount %s: %w", target, err)
		}
	}
	if err := chmodIfExists(filepath.Join(runDir, "codex", "auth.json"), 0o644); err != nil {
		return fmt.Errorf("prepare codex auth readability: %w", err)
	}
	return nil
}

func chownTree(root string, uid, gid int) error {
	if err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return fmt.Errorf("walk %s: %w", path, err)
		}
		if err := os.Lchown(path, uid, gid); err != nil {
			return fmt.Errorf("lchown %s: %w", path, err)
		}
		return nil
	}); err != nil {
		return fmt.Errorf("chown tree %s: %w", root, err)
	}
	return nil
}

func chmodIfExists(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("chmod %s: %w", path, err)
	}
	return nil
}

func sanitizeContainerName(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
		case r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		default:
			b.WriteByte('-')
		}
	}
	out := strings.Trim(b.String(), "-_")
	if out == "" {
		out = "rascal-run"
	}
	if len(out) > 63 {
		return out[:63]
	}
	return out
}

func unwrapSyscallError(err error) error {
	var pathErr *os.PathError
	if errors.As(err, &pathErr) {
		return pathErr
	}
	var execErr *exec.Error
	if errors.As(err, &execErr) {
		return execErr
	}
	var statusErr syscall.Errno
	if errors.As(err, &statusErr) {
		return statusErr
	}
	return err
}
