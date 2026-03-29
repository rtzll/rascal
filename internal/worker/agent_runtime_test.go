package worker

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
	"github.com/rtzll/rascal/internal/runtime"
)

type fakeExecutor struct {
	lookPathFn     func(name string) error
	combinedFn     func(dir string, extraEnv []string, name string, args ...string) (string, error)
	runFn          func(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
	runWithInputFn func(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error
}

func (f fakeExecutor) LookPath(name string) error {
	if f.lookPathFn != nil {
		return f.lookPathFn(name)
	}
	return nil
}

func (f fakeExecutor) CombinedOutput(dir string, extraEnv []string, name string, args ...string) (string, error) {
	if f.combinedFn != nil {
		return f.combinedFn(dir, extraEnv, name, args...)
	}
	return "", nil
}

func (f fakeExecutor) Run(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error {
	if f.runFn != nil {
		return f.runFn(dir, extraEnv, stdout, stderr, name, args...)
	}
	return nil
}

func (f fakeExecutor) RunWithInput(dir string, extraEnv []string, stdin io.Reader, stdout, stderr io.Writer, name string, args ...string) error {
	if f.runWithInputFn != nil {
		return f.runWithInputFn(dir, extraEnv, stdin, stdout, stderr, name, args...)
	}
	return nil
}

func TestGooseRunArgs(t *testing.T) {
	cfg := Config{
		InstructionsPath: "/tmp/instructions.md",
		TaskSession:      runner.TaskSessionSpec{Mode: runtime.SessionModeOff},
	}

	args := GooseRunArgs(cfg, false)
	argsText := strings.Join(args, " ")
	if !strings.Contains(argsText, "--no-session") {
		t.Fatalf("GooseRunArgs() = %q, want --no-session", argsText)
	}

	cfg.TaskSession = runner.TaskSessionSpec{
		Mode:             runtime.SessionModeAll,
		Resume:           true,
		RuntimeSessionID: "session-123",
	}
	args = GooseRunArgs(cfg, true)
	argsText = strings.Join(args, " ")
	for _, want := range []string{"--name", "session-123", "--resume"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("GooseRunArgs() = %q, missing %q", argsText, want)
		}
	}
}

func TestCodexRunArgs(t *testing.T) {
	cfg := Config{AgentOutputPath: "/tmp/agent_output.txt"}

	args := CodexRunArgs(cfg)
	argsText := strings.Join(args, " ")
	for _, want := range []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-o", "/tmp/agent_output.txt", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("CodexRunArgs() = %q, missing %q", argsText, want)
		}
	}

	cfg.TaskSession = runner.TaskSessionSpec{
		Mode:             runtime.SessionModeAll,
		Resume:           true,
		RuntimeSessionID: "session-abc",
	}
	args = CodexRunArgs(cfg)
	argsText = strings.Join(args, " ")
	for _, want := range []string{"exec", "resume", "session-abc"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("CodexRunArgs() = %q, missing %q", argsText, want)
		}
	}
}

func TestClaudeRunArgs(t *testing.T) {
	cfg := Config{AgentOutputPath: "/tmp/output.txt"}

	args := ClaudeRunArgs(cfg, false)
	argsText := strings.Join(args, " ")
	if strings.Contains(argsText, "--resume") {
		t.Fatalf("ClaudeRunArgs() fresh args should not resume: %q", argsText)
	}
	for _, want := range []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "-o", "/tmp/output.txt", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("ClaudeRunArgs() = %q, missing %q", argsText, want)
		}
	}

	cfg.TaskSession = runner.TaskSessionSpec{
		Mode:             runtime.SessionModeAll,
		Resume:           true,
		RuntimeSessionID: "sess-42",
	}
	args = ClaudeRunArgs(cfg, true)
	argsText = strings.Join(args, " ")
	if !strings.Contains(argsText, "--resume sess-42") {
		t.Fatalf("ClaudeRunArgs() = %q, want resume session", argsText)
	}
}

func TestRunCodexFreshSession(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	sessionPath := filepath.Join(codexHome, "sessions", "2026", "03", "session.jsonl")
	cfg := Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentRuntime:     runtime.RuntimeCodex,
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("MkdirAll() sessions error = %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "codex"), 0o755); err != nil {
		t.Fatalf("MkdirAll() auth dir error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() auth error = %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("WriteFile() instructions error = %v", err)
	}

	var gotArgs []string
	var gotInput string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, _ []string, stdin io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "codex" {
				t.Fatalf("command = %q, want codex", name)
			}
			gotArgs = append([]string(nil), args...)
			input, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			gotInput = string(input)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("final codex response"), 0o644); err != nil {
				t.Fatalf("WriteFile() agent output error = %v", err)
			}
			sessionData := strings.Join([]string{
				`{"type":"session_meta","payload":{"id":"session-123"}}`,
				`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":30,"reasoning_output_tokens":10,"total_tokens":150},"total_token_usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":30,"reasoning_output_tokens":10,"total_tokens":150}}}}`,
			}, "\n") + "\n"
			if err := os.WriteFile(sessionPath, []byte(sessionData), 0o644); err != nil {
				t.Fatalf("WriteFile() session error = %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	output, sessionID, err := RunCodex(ex, cfg)
	if err != nil {
		t.Fatalf("RunCodex() error = %v", err)
	}
	if output != "final codex response" {
		t.Fatalf("output = %q, want final codex response", output)
	}
	if sessionID != "session-123" {
		t.Fatalf("sessionID = %q, want session-123", sessionID)
	}
	if gotInput != "do thing" {
		t.Fatalf("stdin = %q, want %q", gotInput, "do thing")
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("args = %q, missing %q", argsText, want)
		}
	}

	usage, ok, err := runsummary.ReadRecordedTokenUsage(filepath.Join(root, runsummary.RecordedTokenUsageFile))
	if err != nil {
		t.Fatalf("ReadRecordedTokenUsage() error = %v", err)
	}
	if !ok || usage.TotalTokens != 150 {
		t.Fatalf("recorded usage = %#v, ok=%t; want total tokens 150", usage, ok)
	}
}

func TestRunClaudeFreshSession(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeClaude,
	}
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatalf("MkdirAll() claude dir error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude", "oauth_token"), []byte("test-oauth-token"), 0o600); err != nil {
		t.Fatalf("WriteFile() token error = %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("WriteFile() instructions error = %v", err)
	}

	var gotArgs []string
	var gotEnv []string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, env []string, stdin io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "claude" {
				t.Fatalf("command = %q, want claude", name)
			}
			gotArgs = append([]string(nil), args...)
			gotEnv = append([]string(nil), env...)
			input, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatalf("ReadAll() error = %v", err)
			}
			if string(input) != "do thing" {
				t.Fatalf("stdin = %q, want %q", string(input), "do thing")
			}
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("final claude response"), 0o644); err != nil {
				t.Fatalf("WriteFile() agent output error = %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake claude output: %w", err)
			}
			return nil
		},
	}

	output, sessionID, err := RunClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunClaude() error = %v", err)
	}
	if output != "final claude response" {
		t.Fatalf("output = %q, want final claude response", output)
	}
	if sessionID != "" {
		t.Fatalf("sessionID = %q, want empty", sessionID)
	}
	if !containsString(gotEnv, "CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token") {
		t.Fatalf("env = %v, want oauth token", gotEnv)
	}
	argsText := strings.Join(gotArgs, " ")
	if strings.Contains(argsText, "--resume") {
		t.Fatalf("args = %q, should not include resume", argsText)
	}
}

func TestGooseRuntimeEnvLoadsClaudeToken(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		MetaDir:         root,
		ClaudeConfigDir: filepath.Join(root, "claude"),
	}
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude", "oauth_token"), []byte("token-123"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	env, err := gooseRuntimeEnv(cfg, runtime.RuntimeGooseClaude)
	if err != nil {
		t.Fatalf("gooseRuntimeEnv() error = %v", err)
	}
	if len(env) != 1 || env[0] != "CLAUDE_CODE_OAUTH_TOKEN=token-123" {
		t.Fatalf("gooseRuntimeEnv() = %v, want oauth token env", env)
	}

	env, err = gooseRuntimeEnv(cfg, runtime.RuntimeGooseCodex)
	if err != nil {
		t.Fatalf("gooseRuntimeEnv() codex error = %v", err)
	}
	if len(env) != 0 {
		t.Fatalf("gooseRuntimeEnv() codex = %v, want nil/empty", env)
	}
}

func TestEnsureCodexHomeCopiesAuth(t *testing.T) {
	root := t.TempDir()
	cfg := Config{
		MetaDir:   root,
		CodexHome: filepath.Join(root, "codex-home"),
	}
	sourceDir := filepath.Join(root, "codex")
	if err := os.MkdirAll(sourceDir, 0o755); err != nil {
		t.Fatalf("MkdirAll() source dir error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "auth.json"), []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("WriteFile() source auth error = %v", err)
	}

	if err := ensureCodexHome(cfg); err != nil {
		t.Fatalf("ensureCodexHome() error = %v", err)
	}
	data, err := os.ReadFile(filepath.Join(cfg.CodexHome, "auth.json"))
	if err != nil {
		t.Fatalf("ReadFile() copied auth error = %v", err)
	}
	if string(data) != `{"token":"abc"}` {
		t.Fatalf("copied auth = %q, want original content", string(data))
	}
}

func TestLoadAgentOutputPrefersFileThenFallsBackToLog(t *testing.T) {
	root := t.TempDir()
	outputPath := filepath.Join(root, "agent_output.txt")
	logPath := filepath.Join(root, "agent.ndjson")

	if err := os.WriteFile(outputPath, []byte("structured output"), 0o644); err != nil {
		t.Fatalf("WriteFile() output error = %v", err)
	}
	if err := os.WriteFile(logPath, []byte("fallback log"), 0o644); err != nil {
		t.Fatalf("WriteFile() log error = %v", err)
	}

	output, err := loadAgentOutput(outputPath, logPath, "codex")
	if err != nil {
		t.Fatalf("loadAgentOutput() error = %v", err)
	}
	if output != "structured output" {
		t.Fatalf("output = %q, want structured output", output)
	}

	if err := os.WriteFile(outputPath, []byte("   "), 0o644); err != nil {
		t.Fatalf("WriteFile() blank output error = %v", err)
	}
	output, err = loadAgentOutput(outputPath, logPath, "codex")
	if err != nil {
		t.Fatalf("loadAgentOutput() fallback error = %v", err)
	}
	if output != "fallback log" {
		t.Fatalf("fallback output = %q, want fallback log", output)
	}
}

func TestDiscoverLatestCodexSessionIDPrefersNewestFile(t *testing.T) {
	root := filepath.Join(t.TempDir(), "sessions")
	oldPath := filepath.Join(root, "2026", "03", "old.jsonl")
	newPath := filepath.Join(root, "2026", "03", "new.jsonl")
	for _, path := range []string{oldPath, newPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("MkdirAll() error = %v", err)
		}
	}
	if err := os.WriteFile(oldPath, []byte(`{"type":"session_meta","payload":{"id":"old-session"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() old session error = %v", err)
	}
	if err := os.WriteFile(newPath, []byte(`{"type":"session_meta","payload":{"id":"new-session"}}`+"\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() new session error = %v", err)
	}
	now := time.Now()
	if err := os.Chtimes(oldPath, now.Add(-time.Hour), now.Add(-time.Hour)); err != nil {
		t.Fatalf("Chtimes() old session error = %v", err)
	}
	if err := os.Chtimes(newPath, now, now); err != nil {
		t.Fatalf("Chtimes() new session error = %v", err)
	}

	sessionID, err := discoverLatestCodexSessionID(filepath.Dir(root))
	if err != nil {
		t.Fatalf("discoverLatestCodexSessionID() error = %v", err)
	}
	if sessionID != "new-session" {
		t.Fatalf("sessionID = %q, want new-session", sessionID)
	}
}

func TestResetGooseSessionRootCreatesRootWhenMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-goose-root")
	if err := ResetGooseSessionRoot(root); err != nil {
		t.Fatalf("ResetGooseSessionRoot() error = %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("root = %s, want directory", root)
	}
}

func TestIsSessionResumeFailureDetectsMissingNamedSession(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), "agent.ndjson")
	if err := os.WriteFile(logPath, []byte("Error: No session found with name 'rascal-owner-repo-task-abc123'\n"), 0o644); err != nil {
		t.Fatalf("WriteFile() log error = %v", err)
	}
	if !IsSessionResumeFailure(errors.New("exit status 1"), logPath) {
		t.Fatal("IsSessionResumeFailure() = false, want true")
	}
}

func TestTaskSubjectAndConventionalTitle(t *testing.T) {
	if got := TaskSubject("  tighten   worker runtime tests \n", "fallback"); got != "tighten worker runtime tests" {
		t.Fatalf("TaskSubject() = %q", got)
	}
	if got := TaskSubject(strings.Repeat("x", 80), "fallback"); len(got) != 58 || !strings.HasSuffix(got, "...") {
		t.Fatalf("TaskSubject() truncation = %q", got)
	}
	if !IsConventionalTitle("test(worker): add focused runtime coverage") {
		t.Fatal("IsConventionalTitle() should accept conventional title")
	}
	if IsConventionalTitle("not a conventional title") {
		t.Fatal("IsConventionalTitle() should reject plain title")
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
