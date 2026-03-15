package worker

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runsummary"
)

type gooseSessionInfo struct {
	Name string `json:"name"`
}

func resolveGitIdentity(ex CommandExecutor) (string, string, error) {
	out, err := runCommand(ex, "", nil, "gh", "api", "user")
	if err != nil {
		return "", "", fmt.Errorf("query GitHub user: %w", err)
	}
	var payload struct {
		Login string `json:"login"`
	}
	if err := json.Unmarshal([]byte(out), &payload); err != nil {
		return "", "", fmt.Errorf("decode GitHub user response: %w", err)
	}
	login := strings.TrimSpace(payload.Login)
	if login == "" {
		return "", "", fmt.Errorf("failed to parse GitHub login from token owner")
	}
	return login, login + "@users.noreply.github.com", nil
}

func checkoutRepo(ex CommandExecutor, cfg Config) error {
	repoURL := fmt.Sprintf("https://x-access-token:%s@github.com/%s.git", cfg.GitHubToken, cfg.Repo)
	if _, err := os.Stat(filepath.Join(cfg.RepoDir, ".git")); err == nil {
		log.Printf("[%s] repo already present, refreshing", nowUTC())
		if _, err := runCommand(ex, "", nil, "git", "-C", cfg.RepoDir, "fetch", "--all", "--prune"); err != nil {
			return fmt.Errorf("refresh existing checkout: %w", err)
		}
	} else {
		log.Printf("[%s] cloning %s", nowUTC(), cfg.Repo)
		if _, err := runCommand(ex, "", nil, "git", "clone", repoURL, cfg.RepoDir); err != nil {
			return fmt.Errorf("clone repository: %w", err)
		}
	}

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "fetch", "origin", cfg.BaseBranch, cfg.HeadBranch); err != nil {
		log.Printf("[%s] git fetch warning: %v", nowUTC(), err)
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.BaseBranch); err != nil {
		if _, createErr := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.BaseBranch, "origin/"+cfg.BaseBranch); createErr != nil {
			return fmt.Errorf("checkout base branch %s: %w", cfg.BaseBranch, createErr)
		}
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "pull", "--ff-only", "origin", cfg.BaseBranch); err != nil {
		log.Printf("[%s] git pull warning: %v", nowUTC(), err)
	}

	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "rev-parse", "--verify", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout head branch %s: %w", cfg.HeadBranch, err)
		}
		return nil
	}
	if _, err := runCommand(ex, cfg.RepoDir, nil, "git", "ls-remote", "--exit-code", "--heads", "origin", cfg.HeadBranch); err == nil {
		_, err = runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch, "origin/"+cfg.HeadBranch)
		if err != nil {
			return fmt.Errorf("checkout remote head branch %s: %w", cfg.HeadBranch, err)
		}
		return nil
	}
	_, err := runCommand(ex, cfg.RepoDir, nil, "git", "checkout", "-b", cfg.HeadBranch)
	if err != nil {
		return fmt.Errorf("create head branch %s: %w", cfg.HeadBranch, err)
	}
	return nil
}

func runAgent(ex CommandExecutor, cfg Config) (string, string, error) {
	switch configuredAgentRuntime(cfg) {
	case agent.BackendCodex:
		return RunCodex(ex, cfg)
	default:
		return RunGoose(ex, cfg)
	}
}

func RunGoose(ex CommandExecutor, cfg Config) (string, string, error) {
	sessionID := configuredRuntimeSessionID(cfg)
	sessionMode := configuredSessionMode(cfg)
	sessionKey := configuredSessionKey(cfg)
	resume := configuredSessionResume(cfg)
	if resume && sessionID != "" {
		exists, err := gooseSessionExists(ex, cfg, sessionID)
		if err != nil {
			log.Printf("[%s] goose session preflight warning: name=%s error=%v", nowUTC(), sessionID, err)
		} else if !exists {
			log.Printf("[%s] goose session missing; starting fresh session name=%s", nowUTC(), sessionID)
			resume = false
		}
	}

	log.Printf("[%s] running goose (backend=%s debug=%t session_mode=%s session_key=%s session_name=%s resume=%t path_root=%s)",
		nowUTC(),
		configuredAgentRuntime(cfg),
		cfg.GooseDebug,
		sessionMode,
		sessionKey,
		sessionID,
		resume,
		cfg.GoosePathRoot,
	)

	firstAttemptArgs := GooseRunArgs(cfg, resume)
	if err := RunGooseOnce(ex, cfg, firstAttemptArgs); err != nil {
		if resume && IsSessionResumeFailure(err, cfg.GooseLogPath) {
			log.Printf("[%s] goose session resume failed; falling back to fresh session name=%s reason=%s", nowUTC(), sessionID, strings.TrimSpace(err.Error()))
			if resetErr := ResetGooseSessionRoot(cfg.GoosePathRoot); resetErr != nil {
				log.Printf("[%s] goose session reset warning: %v", nowUTC(), resetErr)
			}
			fallbackArgs := GooseRunArgs(cfg, false)
			if retryErr := RunGooseOnce(ex, cfg, fallbackArgs); retryErr != nil {
				if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
					log.Printf("[%s] goose fallback log write warning: %v", nowUTC(), writeErr)
				}
				return "", sessionID, fmt.Errorf("goose run failed after session fallback: %w", retryErr)
			}
			log.Printf("[%s] goose session fallback succeeded; started fresh session name=%s", nowUTC(), sessionID)
		} else {
			if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
				log.Printf("[%s] goose log write warning: %v", nowUTC(), writeErr)
			}
			return "", sessionID, fmt.Errorf("goose run failed: %w", err)
		}
	}
	data, err := os.ReadFile(cfg.GooseLogPath)
	if err != nil {
		return "", sessionID, fmt.Errorf("read goose log: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "(no goose output captured)", sessionID, nil
	}
	return string(data), sessionID, nil
}

func GooseRunArgs(cfg Config, resume bool) []string {
	args := []string{"run"}
	if configuredSessionMode(cfg) != agent.SessionModeOff && configuredRuntimeSessionID(cfg) != "" {
		args = append(args, "--name", configuredRuntimeSessionID(cfg))
		if resume {
			args = append(args, "--resume")
		}
	} else {
		args = append(args, "--no-session")
	}
	args = append(args, "-i", cfg.InstructionsPath, "--output-format", "stream-json")
	if cfg.GooseDebug {
		args = append(args, "--debug")
	}
	return args
}

func RunGooseOnce(ex CommandExecutor, cfg Config, args []string) (err error) {
	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open goose log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close goose log: %w", closeErr)
		}
	}()

	env := []string{}
	if cfg.GooseDebug {
		env = append(env, "GOOSE_CODEX_DEBUG=1")
	}
	if err := ex.Run(cfg.RepoDir, env, logFile, logFile, "goose", args...); err != nil {
		return fmt.Errorf("run goose command: %w", err)
	}
	return nil
}

func RunCodex(ex CommandExecutor, cfg Config) (output string, sessionID string, err error) {
	if err := ensureCodexHome(cfg); err != nil {
		return "", "", fmt.Errorf("ensure codex home: %w", err)
	}

	instructions, err := os.ReadFile(cfg.InstructionsPath)
	if err != nil {
		return "", "", fmt.Errorf("read instructions: %w", err)
	}

	args := CodexRunArgs(cfg)
	log.Printf("[%s] running codex (backend=%s session_mode=%s session_key=%s session_id=%s resume=%t home=%s)",
		nowUTC(),
		configuredAgentRuntime(cfg),
		configuredSessionMode(cfg),
		configuredSessionKey(cfg),
		configuredRuntimeSessionID(cfg),
		configuredSessionResume(cfg) && strings.TrimSpace(configuredRuntimeSessionID(cfg)) != "",
		cfg.CodexHome,
	)

	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("open codex log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close codex log: %w", closeErr)
		}
	}()

	if err := ex.RunWithInput(cfg.RepoDir, nil, strings.NewReader(string(instructions)), logFile, logFile, "codex", args...); err != nil {
		sessionID, discoverErr := discoverLatestCodexSessionID(cfg.CodexHome)
		if discoverErr != nil {
			log.Printf("[%s] codex session discovery warning: %v", nowUTC(), discoverErr)
		}
		return "", sessionID, fmt.Errorf("codex run failed: %w", err)
	}

	sessionID, err = discoverLatestCodexSessionID(cfg.CodexHome)
	if err != nil {
		return "", "", fmt.Errorf("discover codex session: %w", err)
	}

	output, err = loadAgentOutput(cfg.AgentOutputPath, cfg.GooseLogPath, "codex")
	if err != nil {
		return "", sessionID, err
	}
	return output, sessionID, nil
}

func CodexRunArgs(cfg Config) []string {
	args := []string{"exec"}
	sessionID := strings.TrimSpace(configuredRuntimeSessionID(cfg))
	if configuredSessionResume(cfg) && sessionID != "" {
		args = append(args, "resume")
	}
	args = append(args, "--json", "--full-auto", "--skip-git-repo-check", "-o", cfg.AgentOutputPath)
	if configuredSessionResume(cfg) && sessionID != "" {
		args = append(args, sessionID)
	} else {
		args = append(args, "-s", "workspace-write")
	}
	args = append(args, "-")
	return args
}

func ensureCodexHome(cfg Config) error {
	if err := os.MkdirAll(cfg.CodexHome, 0o755); err != nil {
		return fmt.Errorf("create codex home: %w", err)
	}
	sourcePath := filepath.Join(cfg.MetaDir, "codex", defaultCodexAuthFile)
	if _, err := os.Stat(sourcePath); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return fmt.Errorf("stat codex auth: %w", err)
	}
	targetPath := filepath.Join(cfg.CodexHome, defaultCodexAuthFile)
	if samePath(sourcePath, targetPath) {
		return nil
	}
	if err := copyFile(sourcePath, targetPath, 0o600); err != nil {
		return fmt.Errorf("copy codex auth into home: %w", err)
	}
	return nil
}

func loadAgentOutput(outputPath, fallbackLogPath, backend string) (string, error) {
	if data, err := os.ReadFile(outputPath); err == nil {
		if text := strings.TrimSpace(string(data)); text != "" {
			return text, nil
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent output: %w", err)
	}

	data, err := os.ReadFile(fallbackLogPath)
	if err != nil {
		return "", fmt.Errorf("read %s log: %w", backend, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return fmt.Sprintf("(no %s output captured)", backend), nil
	}
	return string(data), nil
}

func discoverLatestCodexSessionID(codexHome string) (string, error) {
	sessionFiles, err := listCodexSessionFiles(filepath.Join(strings.TrimSpace(codexHome), defaultCodexSessionDir))
	if err != nil {
		return "", fmt.Errorf("list codex session files: %w", err)
	}
	for _, sessionFile := range sessionFiles {
		sessionID, err := parseCodexSessionID(sessionFile)
		if err != nil {
			continue
		}
		if strings.TrimSpace(sessionID) != "" {
			return strings.TrimSpace(sessionID), nil
		}
	}
	return "", nil
}

func listCodexSessionFiles(root string) ([]string, error) {
	if strings.TrimSpace(root) == "" {
		return nil, nil
	}
	if _, err := os.Stat(root); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("stat codex sessions root: %w", err)
	}

	type sessionFile struct {
		path    string
		modTime time.Time
	}
	var sessionFiles []sessionFile
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return fmt.Errorf("walk codex sessions: %w", err)
		}
		if info.IsDir() || filepath.Ext(path) != ".jsonl" {
			return nil
		}
		sessionFiles = append(sessionFiles, sessionFile{path: path, modTime: info.ModTime()})
		return nil
	}); err != nil {
		return nil, fmt.Errorf("walk codex sessions: %w", err)
	}

	sort.Slice(sessionFiles, func(i, j int) bool {
		if sessionFiles[i].modTime.Equal(sessionFiles[j].modTime) {
			return sessionFiles[i].path > sessionFiles[j].path
		}
		return sessionFiles[i].modTime.After(sessionFiles[j].modTime)
	})

	paths := make([]string, 0, len(sessionFiles))
	for _, sessionFile := range sessionFiles {
		paths = append(paths, sessionFile.path)
	}
	return paths, nil
}

func parseCodexSessionID(path string) (sessionID string, err error) {
	file, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("open codex session file: %w", err)
	}
	defer func() {
		if closeErr := file.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close codex session file: %w", closeErr)
		}
	}()

	scanner := bufio.NewScanner(file)
	if !scanner.Scan() {
		if err := scanner.Err(); err != nil {
			return "", fmt.Errorf("read codex session file: %w", err)
		}
		return "", nil
	}

	var record struct {
		Type    string `json:"type"`
		Payload struct {
			ID string `json:"id"`
		} `json:"payload"`
	}
	if err := json.Unmarshal(scanner.Bytes(), &record); err != nil {
		return "", fmt.Errorf("decode codex session metadata: %w", err)
	}
	return strings.TrimSpace(record.Payload.ID), nil
}

func samePath(left, right string) bool {
	return filepath.Clean(strings.TrimSpace(left)) == filepath.Clean(strings.TrimSpace(right))
}

func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return fmt.Errorf("open source file %s: %w", src, err)
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close source file %s: %w", src, closeErr)
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return fmt.Errorf("open destination file %s: %w", dst, err)
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close destination file %s: %w", dst, closeErr)
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return fmt.Errorf("copy %s to %s: %w", src, dst, err)
	}
	return nil
}

func gooseSessionExists(ex CommandExecutor, cfg Config, name string) (bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "goose", "session", "list", "--format", "json")
	if err != nil {
		return false, fmt.Errorf("list goose sessions: %w", err)
	}
	var sessions []gooseSessionInfo
	if err := json.Unmarshal([]byte(out), &sessions); err != nil {
		return false, fmt.Errorf("decode goose session list: %w", err)
	}
	for _, session := range sessions {
		if strings.TrimSpace(session.Name) == name {
			return true, nil
		}
	}
	return false, nil
}

func ensureGooseLogHasError(path string) error {
	if stat, err := os.Stat(path); err == nil && stat.Size() == 0 {
		if err := os.WriteFile(path, []byte(`{"event":"error","message":"goose run failed"}`+"\n"), 0o644); err != nil {
			return fmt.Errorf("write goose fallback error log: %w", err)
		}
	}
	return nil
}

func ResetGooseSessionRoot(path string) error {
	path = strings.TrimSpace(path)
	if path == "" {
		return nil
	}
	if err := os.MkdirAll(path, 0o755); err != nil {
		return fmt.Errorf("recreate goose session root: %w", err)
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return fmt.Errorf("read goose session root: %w", err)
	}
	for _, entry := range entries {
		child := filepath.Join(path, entry.Name())
		if err := os.RemoveAll(child); err != nil && !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("remove goose session root entry %s: %w", child, err)
		}
	}
	return nil
}

func IsSessionResumeFailure(err error, logPath string) bool {
	var b strings.Builder
	if err != nil {
		b.WriteString(err.Error())
	}
	if data, readErr := os.ReadFile(logPath); readErr == nil {
		if b.Len() > 0 {
			b.WriteByte('\n')
		}
		b.Write(data)
	}
	text := strings.ToLower(b.String())
	if text == "" {
		return false
	}
	hasSessionContext := strings.Contains(text, "session") || strings.Contains(text, "resume")
	if !hasSessionContext {
		return false
	}
	for _, marker := range []string{
		"not found",
		"no session found",
		"no such file",
		"no existing",
		"cannot find",
		"can't find",
		"does not exist",
		"missing",
		"corrupt",
		"invalid",
		"malformed",
		"failed to load",
		"failed loading",
		"decode",
		"deserialize",
		"unmarshal",
		"state",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
}

func LoadAgentCommitMessage(path string) (title, body string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", nil
		}
		return "", "", fmt.Errorf("read commit message: %w", err)
	}
	title = firstNonEmptyLine(string(data))
	body, err = runsummary.ParseCommitBody(data)
	if err != nil {
		return "", "", fmt.Errorf("parse commit body: %w", err)
	}
	return title, body, nil
}

func NormalizeRepoLocalMetaArtifacts(cfg Config) error {
	repoDir := strings.TrimSpace(cfg.RepoDir)
	if repoDir == "" {
		return nil
	}
	repoLocalMetaDir := filepath.Join(repoDir, "rascal-meta")
	repoLocalCommitPath := filepath.Join(repoLocalMetaDir, defaultCommitMsgFile)
	commitPath := strings.TrimSpace(cfg.CommitMsgPath)
	if commitPath != "" && commitPath != repoLocalCommitPath {
		if data, err := os.ReadFile(repoLocalCommitPath); err == nil {
			if _, statErr := os.Stat(commitPath); errors.Is(statErr, os.ErrNotExist) {
				if err := os.MkdirAll(filepath.Dir(commitPath), 0o755); err != nil {
					return fmt.Errorf("create commit message directory: %w", err)
				}
				if err := os.WriteFile(commitPath, data, 0o644); err != nil {
					return fmt.Errorf("adopt repo-local commit message: %w", err)
				}
				log.Printf("[%s] adopted repo-local commit message into %s", nowUTC(), commitPath)
			} else if statErr != nil {
				return fmt.Errorf("stat commit message path: %w", statErr)
			}
		} else if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read repo-local commit message: %w", err)
		}
	}
	if err := os.RemoveAll(repoLocalMetaDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove repo-local rascal-meta: %w", err)
	}
	return nil
}

func firstNonEmptyLine(s string) string {
	lines := strings.Split(s, "\n")
	for _, line := range lines {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line != "" {
			return line
		}
	}
	return ""
}

func loadPRView(ex CommandExecutor, cfg Config) (prView, bool, error) {
	out, err := runCommand(ex, cfg.RepoDir, nil, "gh", "pr", "view", cfg.HeadBranch, "--repo", cfg.Repo, "--json", "number,url")
	if err != nil {
		return prView{}, false, nil
	}
	var view prView
	if err := json.Unmarshal([]byte(out), &view); err != nil {
		return prView{}, false, fmt.Errorf("decode gh pr view output: %w", err)
	}
	return view, true, nil
}

func RunStage(name string, fn func() error) error {
	start := time.Now()
	log.Printf("[%s] stage_start stage=%s", nowUTC(), name)
	err := fn()
	duration := time.Since(start).Round(time.Millisecond)
	if err != nil {
		log.Printf("[%s] stage_fail stage=%s duration=%s error=%v", nowUTC(), name, duration, err)
		return fmt.Errorf("stage %s: %w", name, err)
	}
	log.Printf("[%s] stage_done stage=%s duration=%s", nowUTC(), name, duration)
	return nil
}

func runCommand(ex CommandExecutor, dir string, extraEnv []string, name string, args ...string) (string, error) {
	out, err := ex.CombinedOutput(dir, extraEnv, name, args...)
	if err != nil {
		return out, fmt.Errorf("run %s %s: %w", name, strings.Join(args, " "), err)
	}
	return out, nil
}

func TaskSubject(task, fallback string) string {
	s := strings.Join(strings.Fields(strings.ReplaceAll(strings.TrimSpace(task), "\r", " ")), " ")
	if s == "" {
		s = strings.TrimSpace(fallback)
	}
	if len(s) > 58 {
		return s[:55] + "..."
	}
	return s
}

func IsConventionalTitle(title string) bool {
	return convCommitPattern.MatchString(strings.TrimSpace(title))
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func BuildInfoSummary() string {
	return fmt.Sprintf("version=%s commit=%s built=%s", BuildVersion, BuildCommit, BuildTime)
}
