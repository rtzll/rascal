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

	"github.com/rtzll/rascal/internal/runtime"
)

type gooseSessionInfo struct {
	Name string `json:"name"`
}

func runAgent(ex CommandExecutor, cfg Config) (string, string, error) {
	switch configuredAgentRuntime(cfg) {
	case runtime.RuntimeCodex:
		return RunCodex(ex, cfg)
	case runtime.RuntimeClaude:
		return RunClaude(ex, cfg)
	case runtime.RuntimeGooseClaude:
		return RunGooseClaude(ex, cfg)
	default:
		return RunGooseCodex(ex, cfg)
	}
}

func RunGooseCodex(ex CommandExecutor, cfg Config) (string, string, error) {
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
	if configuredSessionMode(cfg) != runtime.SessionModeOff && configuredRuntimeSessionID(cfg) != "" {
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
	// The Docker runner is already the isolation boundary for Codex tasks.
	// Using Codex's inner sandbox here triggers bwrap/userns failures in the
	// container and prevents the agent from reading the workspace at all.
	args = append(args, "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-o", cfg.AgentOutputPath)
	if configuredSessionResume(cfg) && sessionID != "" {
		args = append(args, sessionID)
	}
	args = append(args, "-")
	return args
}

func RunClaude(ex CommandExecutor, cfg Config) (output string, sessionID string, err error) {
	token, err := loadClaudeOAuthToken(cfg)
	if err != nil {
		return "", "", fmt.Errorf("load claude oauth token: %w", err)
	}

	instructions, err := os.ReadFile(cfg.InstructionsPath)
	if err != nil {
		return "", "", fmt.Errorf("read instructions: %w", err)
	}

	sessionID = configuredRuntimeSessionID(cfg)
	resume := configuredSessionResume(cfg) && strings.TrimSpace(sessionID) != ""

	log.Printf("[%s] running claude (backend=%s session_mode=%s session_key=%s session_id=%s resume=%t config_dir=%s)",
		nowUTC(),
		configuredAgentRuntime(cfg),
		configuredSessionMode(cfg),
		configuredSessionKey(cfg),
		sessionID,
		resume,
		cfg.ClaudeConfigDir,
	)

	args := ClaudeRunArgs(cfg, resume)

	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return "", "", fmt.Errorf("open claude log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close claude log: %w", closeErr)
		}
	}()

	env := []string{}
	if token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}

	if err := ex.RunWithInput(cfg.RepoDir, env, strings.NewReader(string(instructions)), logFile, logFile, "claude", args...); err != nil {
		return "", sessionID, fmt.Errorf("claude run failed: %w", err)
	}

	output, err = loadAgentOutput(cfg.AgentOutputPath, cfg.GooseLogPath, "claude")
	if err != nil {
		return "", sessionID, err
	}
	return output, sessionID, nil
}

func ClaudeRunArgs(cfg Config, resume bool) []string {
	args := []string{"-p"}
	if resume {
		sessionID := strings.TrimSpace(configuredRuntimeSessionID(cfg))
		if sessionID != "" {
			args = append(args, "--resume", sessionID)
		}
	}
	args = append(args, "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "-o", cfg.AgentOutputPath, "-")
	return args
}

func loadClaudeOAuthToken(cfg Config) (string, error) {
	sourcePath := filepath.Join(cfg.MetaDir, "claude", defaultClaudeOAuthFile)
	data, err := os.ReadFile(sourcePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", fmt.Errorf("read claude oauth token: %w", err)
	}
	return strings.TrimSpace(string(data)), nil
}

func RunGooseClaude(ex CommandExecutor, cfg Config) (string, string, error) {
	token, err := loadClaudeOAuthToken(cfg)
	if err != nil {
		return "", "", fmt.Errorf("load claude oauth token: %w", err)
	}

	sessionID := configuredRuntimeSessionID(cfg)
	sessionMode := configuredSessionMode(cfg)
	sessionKey := configuredSessionKey(cfg)
	resume := configuredSessionResume(cfg)
	if resume && sessionID != "" {
		exists, err := gooseSessionExists(ex, cfg, sessionID)
		if err != nil {
			log.Printf("[%s] goose-claude session preflight warning: name=%s error=%v", nowUTC(), sessionID, err)
		} else if !exists {
			log.Printf("[%s] goose-claude session missing; starting fresh session name=%s", nowUTC(), sessionID)
			resume = false
		}
	}

	log.Printf("[%s] running goose-claude (backend=%s debug=%t session_mode=%s session_key=%s session_name=%s resume=%t path_root=%s)",
		nowUTC(),
		configuredAgentRuntime(cfg),
		cfg.GooseDebug,
		sessionMode,
		sessionKey,
		sessionID,
		resume,
		cfg.GoosePathRoot,
	)

	env := []string{}
	if token != "" {
		env = append(env, "CLAUDE_CODE_OAUTH_TOKEN="+token)
	}

	firstAttemptArgs := GooseRunArgs(cfg, resume)
	if err := runGooseClaudeOnce(ex, cfg, firstAttemptArgs, env); err != nil {
		if resume && IsSessionResumeFailure(err, cfg.GooseLogPath) {
			log.Printf("[%s] goose-claude session resume failed; falling back to fresh session name=%s reason=%s", nowUTC(), sessionID, strings.TrimSpace(err.Error()))
			if resetErr := ResetGooseSessionRoot(cfg.GoosePathRoot); resetErr != nil {
				log.Printf("[%s] goose-claude session reset warning: %v", nowUTC(), resetErr)
			}
			fallbackArgs := GooseRunArgs(cfg, false)
			if retryErr := runGooseClaudeOnce(ex, cfg, fallbackArgs, env); retryErr != nil {
				if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
					log.Printf("[%s] goose-claude fallback log write warning: %v", nowUTC(), writeErr)
				}
				return "", sessionID, fmt.Errorf("goose-claude run failed after session fallback: %w", retryErr)
			}
			log.Printf("[%s] goose-claude session fallback succeeded; started fresh session name=%s", nowUTC(), sessionID)
		} else {
			if writeErr := ensureGooseLogHasError(cfg.GooseLogPath); writeErr != nil {
				log.Printf("[%s] goose-claude log write warning: %v", nowUTC(), writeErr)
			}
			return "", sessionID, fmt.Errorf("goose-claude run failed: %w", err)
		}
	}
	data, err := os.ReadFile(cfg.GooseLogPath)
	if err != nil {
		return "", sessionID, fmt.Errorf("read goose-claude log: %w", err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return "(no goose-claude output captured)", sessionID, nil
	}
	return string(data), sessionID, nil
}

func runGooseClaudeOnce(ex CommandExecutor, cfg Config, args, extraEnv []string) (err error) {
	logFile, err := os.OpenFile(cfg.GooseLogPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open goose-claude log: %w", err)
	}
	defer func() {
		if closeErr := logFile.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close goose-claude log: %w", closeErr)
		}
	}()

	env := append([]string{}, extraEnv...)
	if cfg.GooseDebug {
		env = append(env, "GOOSE_CODEX_DEBUG=1")
	}
	if err := ex.Run(cfg.RepoDir, env, logFile, logFile, "goose", args...); err != nil {
		return fmt.Errorf("run goose-claude command: %w", err)
	}
	return nil
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
