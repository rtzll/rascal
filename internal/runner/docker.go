package runner

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// DockerLauncher runs a task inside a Docker container.
type DockerLauncher struct {
	Image       string
	GitHubToken string
}

const (
	// Keep in sync with runner/Dockerfile runtime user UID/GID.
	runtimeUID = 10001
	runtimeGID = 10001
)

func (l DockerLauncher) Start(ctx context.Context, spec Spec) (Result, error) {
	if l.Image == "" {
		return Result{}, fmt.Errorf("docker image is required")
	}
	if err := os.MkdirAll(spec.RunDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create run dir: %w", err)
	}
	workspaceDir := filepath.Join(spec.RunDir, "workspace")
	if err := os.MkdirAll(workspaceDir, 0o755); err != nil {
		return Result{}, fmt.Errorf("create workspace dir: %w", err)
	}
	if err := prepareMountAccess(spec.RunDir, workspaceDir); err != nil {
		return Result{}, err
	}

	logPath := filepath.Join(spec.RunDir, "runner.log")
	logFile, err := os.OpenFile(logPath, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return Result{}, fmt.Errorf("open runner log: %w", err)
	}
	defer logFile.Close()

	_, _ = fmt.Fprintf(logFile, "[%s] starting docker runner image=%s run_id=%s\n", time.Now().UTC().Format(time.RFC3339), l.Image, spec.RunID)

	envPairs := map[string]string{
		"RASCAL_RUN_ID":                spec.RunID,
		"RASCAL_TASK_ID":               spec.TaskID,
		"RASCAL_TASK":                  spec.Task,
		"RASCAL_REPO":                  spec.Repo,
		"RASCAL_BASE_BRANCH":           spec.BaseBranch,
		"RASCAL_HEAD_BRANCH":           spec.HeadBranch,
		"RASCAL_TRIGGER":               spec.Trigger,
		"RASCAL_GOOSE_DEBUG":           strconv.FormatBool(spec.Debug),
		"RASCAL_CONTEXT":               spec.Context,
		"RASCAL_CONTEXT_JSON":          "/rascal-meta/context.json",
		"RASCAL_ISSUE_NUMBER":          strconv.Itoa(spec.IssueNumber),
		"RASCAL_PR_NUMBER":             strconv.Itoa(spec.PRNumber),
		"RASCAL_GOOSE_SESSION_MODE":    NormalizeGooseSessionMode(spec.GooseSessionMode),
		"RASCAL_GOOSE_SESSION_RESUME":  strconv.FormatBool(spec.GooseSessionResume),
		"RASCAL_GOOSE_SESSION_KEY":     strings.TrimSpace(spec.GooseSessionTaskKey),
		"RASCAL_GOOSE_SESSION_NAME":    strings.TrimSpace(spec.GooseSessionName),
		"CODEX_HOME":                   "/rascal-meta/codex",
		"GOOSE_PROVIDER":               "codex",
		"GOOSE_MODEL":                  "gpt-5.4",
		"GOOSE_MODE":                   "auto",
		"GOOSE_DISABLE_KEYRING":        "1",
		"GOOSE_DISABLE_SESSION_NAMING": "true",
		"GOOSE_CONTEXT_STRATEGY":       "summarize",
		"GH_PROMPT_DISABLED":           "1",
		"GIT_TERMINAL_PROMPT":          "0",
	}
	if strings.TrimSpace(l.GitHubToken) != "" {
		envPairs["GH_TOKEN"] = l.GitHubToken
	}

	goosePathRoot := "/rascal-meta/goose"
	sessionDir := strings.TrimSpace(spec.GooseSessionTaskDir)
	if spec.GooseSessionResume && sessionDir != "" {
		if err := os.MkdirAll(sessionDir, 0o755); err != nil {
			return Result{}, fmt.Errorf("create goose session dir: %w", err)
		}
		goosePathRoot = "/rascal-goose-session"
	}
	envPairs["GOOSE_PATH_ROOT"] = goosePathRoot

	containerName := sanitizeContainerName("rascal-" + spec.RunID)
	args := []string{"run", "--rm", "--name", containerName}
	envKeys := make([]string, 0, len(envPairs))
	for k := range envPairs {
		envKeys = append(envKeys, k)
	}
	sort.Strings(envKeys)
	for _, k := range envKeys {
		v := envPairs[k]
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, v))
	}
	args = append(args,
		// Harden container execution against privilege escalation.
		"--security-opt", "no-new-privileges:true",
		"-v", fmt.Sprintf("%s:/rascal-meta", spec.RunDir),
		"-v", fmt.Sprintf("%s:/work", workspaceDir),
	)
	if spec.GooseSessionResume && sessionDir != "" {
		args = append(args, "-v", fmt.Sprintf("%s:%s", sessionDir, goosePathRoot))
	}
	args = append(args, l.Image)

	_, _ = fmt.Fprintf(logFile, "[%s] goose session mode=%s resume=%t key=%s name=%s path_root=%s\n",
		time.Now().UTC().Format(time.RFC3339),
		NormalizeGooseSessionMode(spec.GooseSessionMode),
		spec.GooseSessionResume,
		strings.TrimSpace(spec.GooseSessionTaskKey),
		strings.TrimSpace(spec.GooseSessionName),
		goosePathRoot,
	)

	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	err = cmd.Run()
	if ctx.Err() != nil {
		// Context cancellation can terminate the local docker client before the
		// remote container is fully cleaned up, so force cleanup deterministically.
		forceStopContainer(containerName, logFile)
		err = context.Canceled
	}
	exitCode := 0
	if err != nil {
		if exitErr := new(exec.ExitError); errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			exitCode = -1
		}
	}

	metaPath := filepath.Join(spec.RunDir, "meta.json")
	meta, metaErr := ReadMeta(metaPath)
	if metaErr != nil {
		meta = Meta{
			RunID:      spec.RunID,
			TaskID:     spec.TaskID,
			Repo:       spec.Repo,
			BaseBranch: spec.BaseBranch,
			HeadBranch: spec.HeadBranch,
			ExitCode:   exitCode,
		}
		if err != nil {
			meta.Error = err.Error()
		}
		_ = WriteMeta(metaPath, meta)
	}

	res := Result{
		PRNumber: meta.PRNumber,
		PRURL:    meta.PRURL,
		HeadSHA:  meta.HeadSHA,
		ExitCode: meta.ExitCode,
	}
	if res.ExitCode == 0 {
		res.ExitCode = exitCode
	}

	if err != nil {
		if errors.Is(err, context.Canceled) {
			return res, context.Canceled
		}
		return res, fmt.Errorf("docker runner failed (exit=%d): %w", exitCode, unwrapSyscallError(err))
	}
	if exitCode != 0 {
		return res, fmt.Errorf("docker runner failed with exit code %d", exitCode)
	}
	return res, nil
}

func forceStopContainer(containerName string, logOut io.Writer) {
	if strings.TrimSpace(containerName) == "" {
		return
	}
	stopCmd := exec.Command("docker", "stop", "--time", "5", containerName)
	stopCmd.Stdout = logOut
	stopCmd.Stderr = logOut
	_ = stopCmd.Run()
	rmCmd := exec.Command("docker", "rm", "-f", containerName)
	rmCmd.Stdout = logOut
	rmCmd.Stderr = logOut
	_ = rmCmd.Run()
}

func prepareMountAccess(runDir, workspaceDir string) error {
	if os.Geteuid() == 0 {
		if err := chownTree(runDir, runtimeUID, runtimeGID); err != nil {
			return fmt.Errorf("prepare run dir ownership: %w", err)
		}
		return nil
	}

	// Non-root launcher fallback: make bind mounts writable by the runtime UID.
	for _, target := range []string{runDir, workspaceDir, filepath.Join(runDir, "codex"), filepath.Join(runDir, "goose")} {
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
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		return os.Lchown(path, uid, gid)
	})
}

func chmodIfExists(path string, mode os.FileMode) error {
	if err := os.Chmod(path, mode); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
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
