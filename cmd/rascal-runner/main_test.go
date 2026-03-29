package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/worker"
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
	if f.runFn != nil {
		return f.runFn(dir, extraEnv, stdout, stderr, name, args...)
	}
	return nil
}

func TestRunGooseNoSessionByDefault(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		TaskSession:      runner.TaskSessionSpec{Mode: runtime.SessionModeOff},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunGooseCodex(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	if !strings.Contains(argsText, "--no-session") {
		t.Fatalf("expected --no-session args, got %q", argsText)
	}
	if strings.Contains(argsText, "--name") {
		t.Fatalf("did not expect --name args, got %q", argsText)
	}
}

func TestRunGooseUsesNamedResumeSessionWhenEnabled(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			RuntimeSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return fmt.Sprintf(`[{"name":%q}]`, cfg.TaskSession.RuntimeSessionID), nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunGooseCodex(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"--name", cfg.TaskSession.RuntimeSessionID, "--resume"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "--no-session") {
		t.Fatalf("did not expect --no-session args, got %q", argsText)
	}
}

func TestRunGooseSkipsResumeWhenNamedSessionIsMissing(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-missing",
			RuntimeSessionID: "rascal-owner-repo-task-missing",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return "[]", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunGooseCodex(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	if strings.Contains(argsText, "--resume") {
		t.Fatalf("did not expect --resume args when session is missing, got %q", argsText)
	}
	if !strings.Contains(argsText, "--name "+cfg.TaskSession.RuntimeSessionID) {
		t.Fatalf("expected named fresh session args, got %q", argsText)
	}
}

func TestRunGooseFallsBackToFreshSessionOnResumeStateError(t *testing.T) {
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "goose-sessions")
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    sessionRoot,
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			RuntimeSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}
	beforeInfo, err := os.Stat(sessionRoot)
	if err != nil {
		t.Fatalf("stat session root before run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "stale.json"), []byte("bad"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(sessionRoot, "state", "logs"), 0o755); err != nil {
		t.Fatalf("mkdir nested stale session data: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "state", "logs", "old.log"), []byte("old"), 0o644); err != nil {
		t.Fatalf("write nested stale session data: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var calls [][]string
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return fmt.Sprintf(`[{"name":%q}]`, cfg.TaskSession.RuntimeSessionID), nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			calls = append(calls, append([]string(nil), args...))
			if len(calls) == 1 {
				return errors.New("resume failed: session state missing")
			}
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunGooseCodex(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	if len(calls) != 2 {
		t.Fatalf("expected 2 goose attempts, got %d", len(calls))
	}
	firstArgs := strings.Join(calls[0], " ")
	secondArgs := strings.Join(calls[1], " ")
	if !strings.Contains(firstArgs, "--resume") {
		t.Fatalf("expected first attempt to resume, got %q", firstArgs)
	}
	if strings.Contains(secondArgs, "--resume") {
		t.Fatalf("expected fallback attempt without resume, got %q", secondArgs)
	}
	if _, err := os.Stat(filepath.Join(sessionRoot, "stale.json")); !os.IsNotExist(err) {
		t.Fatalf("expected stale marker to be removed during fallback reset, stat err=%v", err)
	}
	if _, err := os.Stat(filepath.Join(sessionRoot, "state")); !os.IsNotExist(err) {
		t.Fatalf("expected nested session state to be removed during fallback reset, stat err=%v", err)
	}
	afterInfo, err := os.Stat(sessionRoot)
	if err != nil {
		t.Fatalf("stat session root after run: %v", err)
	}
	if !os.SameFile(beforeInfo, afterInfo) {
		t.Fatal("expected fallback reset to preserve the session root mountpoint")
	}
}

func TestRunGooseDoesNotFallbackOnUnrelatedFailure(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			RuntimeSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	calls := 0
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return fmt.Sprintf(`[{"name":%q}]`, cfg.TaskSession.RuntimeSessionID), nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, _ io.Writer, _ io.Writer, _ string, _ ...string) error {
			calls++
			return errors.New("goose transport timeout")
		},
	}

	_, _, err := worker.RunGooseCodex(ex, cfg)
	if err == nil {
		t.Fatal("expected runGoose to fail")
	}
	if calls != 1 {
		t.Fatalf("expected one goose attempt, got %d", calls)
	}
}

func TestRunGooseKeepsResumeWhenSessionPreflightFails(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			RuntimeSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return "", errors.New("session list unavailable")
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunGooseCodex(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	if !strings.Contains(argsText, "--resume") {
		t.Fatalf("expected resume args when session preflight fails, got %q", argsText)
	}
}

func TestRunCodexFreshSession(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	sessionPath := filepath.Join(codexHome, "sessions", "2026", "03", "session.jsonl")
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentRuntime:     runtime.RuntimeCodex,
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex auth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	var gotInput string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, _ []string, stdin io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "codex" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			input, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatalf("read stdin: %v", err)
			}
			gotInput = string(input)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("final codex response"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			sessionData := strings.Join([]string{
				`{"type":"session_meta","payload":{"id":"session-123"}}`,
				`{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":30,"reasoning_output_tokens":10,"total_tokens":150},"total_token_usage":{"input_tokens":120,"cached_input_tokens":40,"output_tokens":30,"reasoning_output_tokens":10,"total_tokens":150}}}}`,
			}, "\n") + "\n"
			if err := os.WriteFile(sessionPath, []byte(sessionData), 0o644); err != nil {
				t.Fatalf("write codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	output, sessionID, err := worker.RunCodex(ex, cfg)
	if err != nil {
		t.Fatalf("runCodex returned error: %v", err)
	}
	if output != "final codex response" {
		t.Fatalf("output = %q, want final codex response", output)
	}
	if sessionID != "session-123" {
		t.Fatalf("sessionID = %q, want session-123", sessionID)
	}
	if gotInput != "do thing" {
		t.Fatalf("codex stdin = %q, want %q", gotInput, "do thing")
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "workspace-write") {
		t.Fatalf("did not expect codex workspace sandbox args, got %q", argsText)
	}
	if strings.Contains(argsText, " resume ") {
		t.Fatalf("did not expect resume args in fresh codex run, got %q", argsText)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatalf("expected codex auth copied into home: %v", err)
	}
	usage, ok, err := runsummary.ReadRecordedTokenUsage(filepath.Join(root, runsummary.RecordedTokenUsageFile))
	if err != nil {
		t.Fatalf("read recorded token usage: %v", err)
	}
	if !ok {
		t.Fatal("expected recorded token usage")
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", usage.TotalTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 40 {
		t.Fatalf("cached_input_tokens = %v, want 40", usage.CachedInputTokens)
	}
}

func TestRunCodexResumeSession(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	sessionPath := filepath.Join(codexHome, "sessions", "2026", "03", "session.jsonl")
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentRuntime:     runtime.RuntimeCodex,
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModeAll,
			Resume:           true,
			RuntimeSessionID: "session-abc",
		},
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex auth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("continue"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}
	initialSessionData := strings.Join([]string{
		`{"type":"session_meta","payload":{"id":"session-abc"}}`,
		`{"type":"event_msg","payload":{"type":"token_count","info":{"total_token_usage":{"input_tokens":80,"cached_input_tokens":20,"output_tokens":20,"reasoning_output_tokens":5,"total_tokens":100}}}}`,
	}, "\n") + "\n"
	if err := os.WriteFile(sessionPath, []byte(initialSessionData), 0o644); err != nil {
		t.Fatalf("write initial codex session: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, _ []string, _ io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "codex" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("continued"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			sessionFile, err := os.OpenFile(sessionPath, os.O_APPEND|os.O_WRONLY, 0o644)
			if err != nil {
				t.Fatalf("open codex session: %v", err)
			}
			defer func() {
				if closeErr := sessionFile.Close(); closeErr != nil {
					t.Fatalf("close codex session: %v", closeErr)
				}
			}()
			if _, err := io.WriteString(sessionFile, `{"type":"event_msg","payload":{"type":"token_count","info":{"last_token_usage":{"input_tokens":35,"cached_input_tokens":10,"output_tokens":15,"reasoning_output_tokens":3,"total_tokens":50},"total_token_usage":{"input_tokens":115,"cached_input_tokens":30,"output_tokens":35,"reasoning_output_tokens":8,"total_tokens":150}}}}`+"\n"); err != nil {
				t.Fatalf("append codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	_, sessionID, err := worker.RunCodex(ex, cfg)
	if err != nil {
		t.Fatalf("runCodex returned error: %v", err)
	}
	if sessionID != "session-abc" {
		t.Fatalf("sessionID = %q, want session-abc", sessionID)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec", "resume", "--json", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "session-abc", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "workspace-write") {
		t.Fatalf("did not expect explicit sandbox arg on resume, got %q", argsText)
	}
	usage, ok, err := runsummary.ReadRecordedTokenUsage(filepath.Join(root, runsummary.RecordedTokenUsageFile))
	if err != nil {
		t.Fatalf("read recorded token usage: %v", err)
	}
	if !ok {
		t.Fatal("expected recorded token usage")
	}
	if usage.TotalTokens != 50 {
		t.Fatalf("total_tokens = %d, want 50", usage.TotalTokens)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 35 {
		t.Fatalf("input_tokens = %v, want 35", usage.InputTokens)
	}
}

func TestRunCodexSkipsRecordedUsageWhenSessionUsageInvalid(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	sessionPath := filepath.Join(codexHome, "sessions", "2026", "03", "session.jsonl")
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentRuntime:     runtime.RuntimeCodex,
	}
	if err := os.MkdirAll(filepath.Dir(sessionPath), 0o755); err != nil {
		t.Fatalf("mkdir codex sessions: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(root, "codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex auth dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	ex := fakeExecutor{
		runWithInputFn: func(_ string, _ []string, _ io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("final codex response"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			sessionData := strings.Join([]string{
				`{"type":"session_meta","payload":{"id":"session-123"}}`,
				`{"type":"event_msg","payload":{"type":"token_count","info":`,
			}, "\n")
			if err := os.WriteFile(sessionPath, []byte(sessionData), 0o644); err != nil {
				t.Fatalf("write codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	if _, _, err := worker.RunCodex(ex, cfg); err != nil {
		t.Fatalf("runCodex returned error: %v", err)
	}
	if _, ok, err := runsummary.ReadRecordedTokenUsage(filepath.Join(root, runsummary.RecordedTokenUsageFile)); err != nil {
		t.Fatalf("read recorded token usage: %v", err)
	} else if ok {
		t.Fatal("did not expect recorded token usage")
	}
}

func TestRunClaudeFreshSession(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeClaude,
	}
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude", "oauth_token"), []byte("test-oauth-token"), 0o600); err != nil {
		t.Fatalf("write claude oauth token: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	var gotInput string
	var gotEnv []string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, env []string, stdin io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "claude" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			gotEnv = append([]string(nil), env...)
			input, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatalf("read stdin: %v", err)
			}
			gotInput = string(input)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("final claude response"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake claude output: %w", err)
			}
			return nil
		},
	}

	output, sessionID, err := worker.RunClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunClaude returned error: %v", err)
	}
	if output != "final claude response" {
		t.Fatalf("output = %q, want final claude response", output)
	}
	if sessionID != "" {
		t.Fatalf("sessionID = %q, want empty for fresh session", sessionID)
	}
	if gotInput != "do thing" {
		t.Fatalf("claude stdin = %q, want %q", gotInput, "do thing")
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"-p", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions", "-o", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "--resume") {
		t.Fatalf("did not expect --resume in fresh claude run, got %q", argsText)
	}
	foundToken := false
	for _, e := range gotEnv {
		if e == "CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Fatalf("expected CLAUDE_CODE_OAUTH_TOKEN in env, got %v", gotEnv)
	}
}

func TestRunClaudeResumeSession(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeClaude,
		TaskSession: runner.TaskSessionSpec{
			Mode:             runtime.SessionModeAll,
			Resume:           true,
			RuntimeSessionID: "session-claude-abc",
		},
	}
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude", "oauth_token"), []byte("test-oauth-token"), 0o600); err != nil {
		t.Fatalf("write claude oauth token: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("continue"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, _ []string, _ io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "claude" {
				t.Fatalf("unexpected command: %s", name)
			}
			gotArgs = append([]string(nil), args...)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("continued"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake claude output: %w", err)
			}
			return nil
		},
	}

	_, sessionID, err := worker.RunClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunClaude returned error: %v", err)
	}
	if sessionID != "session-claude-abc" {
		t.Fatalf("sessionID = %q, want session-claude-abc", sessionID)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"-p", "--resume", "session-claude-abc", "--output-format", "stream-json", "--verbose", "--dangerously-skip-permissions"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
}

func TestRunClaudeNoTokenFile(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeClaude,
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotEnv []string
	ex := fakeExecutor{
		runWithInputFn: func(_ string, env []string, _ io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			gotEnv = append([]string(nil), env...)
			if err := os.WriteFile(cfg.AgentOutputPath, []byte("response"), 0o644); err != nil {
				t.Fatalf("write agent output: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake claude output: %w", err)
			}
			return nil
		},
	}

	_, _, err := worker.RunClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunClaude returned error: %v", err)
	}
	for _, e := range gotEnv {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("did not expect CLAUDE_CODE_OAUTH_TOKEN when no token file exists, got %v", gotEnv)
		}
	}
}

func TestRunGooseClaudeFreshSession(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		GoosePathRoot:    filepath.Join(root, "goose"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeGooseClaude,
	}
	if err := os.MkdirAll(filepath.Join(root, "claude"), 0o755); err != nil {
		t.Fatalf("mkdir claude dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "claude", "oauth_token"), []byte("test-oauth-token"), 0o600); err != nil {
		t.Fatalf("write claude oauth token: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotName string
	var gotArgs []string
	var gotEnv []string
	ex := fakeExecutor{
		runFn: func(_ string, env []string, stdout, _ io.Writer, name string, args ...string) error {
			gotName = name
			gotArgs = append([]string(nil), args...)
			gotEnv = append([]string(nil), env...)
			if _, err := io.WriteString(stdout, `{"event":"message","message":"done"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	output, _, err := worker.RunGooseClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunGooseClaude returned error: %v", err)
	}
	if gotName != "goose" {
		t.Fatalf("expected goose command, got %q", gotName)
	}
	if !strings.Contains(output, "message") {
		t.Fatalf("output = %q, expected goose log content", output)
	}
	argsText := strings.Join(gotArgs, " ")
	if !strings.Contains(argsText, "--no-session") {
		t.Fatalf("expected --no-session in fresh run args, got %q", argsText)
	}
	foundToken := false
	for _, e := range gotEnv {
		if e == "CLAUDE_CODE_OAUTH_TOKEN=test-oauth-token" {
			foundToken = true
		}
	}
	if !foundToken {
		t.Fatalf("expected CLAUDE_CODE_OAUTH_TOKEN in env, got %v", gotEnv)
	}
}

func TestRunGooseClaudeNoTokenFile(t *testing.T) {
	root := t.TempDir()
	cfg := worker.Config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		GoosePathRoot:    filepath.Join(root, "goose"),
		ClaudeConfigDir:  filepath.Join(root, "claude"),
		AgentRuntime:     runtime.RuntimeGooseClaude,
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotEnv []string
	ex := fakeExecutor{
		runFn: func(_ string, env []string, stdout, _ io.Writer, _ string, _ ...string) error {
			gotEnv = append([]string(nil), env...)
			if _, err := io.WriteString(stdout, `{"event":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake goose output: %w", err)
			}
			return nil
		},
	}

	_, _, err := worker.RunGooseClaude(ex, cfg)
	if err != nil {
		t.Fatalf("RunGooseClaude returned error: %v", err)
	}
	for _, e := range gotEnv {
		if strings.HasPrefix(e, "CLAUDE_CODE_OAUTH_TOKEN=") {
			t.Fatalf("did not expect CLAUDE_CODE_OAUTH_TOKEN when no token file exists, got %v", gotEnv)
		}
	}
}

func TestRunEndToEndWithFakeCommands(t *testing.T) {
	root := t.TempDir()
	binDir := filepath.Join(root, "bin")
	stateDir := filepath.Join(root, "state")
	metaDir := filepath.Join(root, "meta")
	workRoot := filepath.Join(root, "work")
	repoDir := filepath.Join(workRoot, "repo")
	for _, dir := range []string{binDir, stateDir, metaDir, workRoot} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}

	writeExe(t, filepath.Join(binDir, "git"), fmt.Sprintf(`#!/usr/bin/env bash
set -eu
state_dir=%q

if [ "$#" -ge 1 ] && [ "$1" = "-C" ]; then
  shift
  repo_dir="$1"
  shift
else
  repo_dir=""
fi

cmd="$1"
shift || true

case "$cmd" in
  clone)
    target="$2"
    mkdir -p "$target/.git"
    exit 0
    ;;
  fetch|pull|checkout|add|commit|push)
    exit 0
    ;;
  status)
    printf ' M touched.txt\n'
    exit 0
    ;;
  rev-parse)
    if [ "$#" -ge 1 ] && [ "$1" = "--verify" ]; then
      exit 1
    fi
    if [ "$#" -ge 1 ] && [ "$1" = "HEAD" ]; then
      printf '0123456789abcdef0123456789abcdef01234567\n'
      exit 0
    fi
    exit 0
    ;;
  rev-list)
    if [ "$#" -ge 3 ] && [ "$1" = "--left-right" ] && [ "$2" = "--count" ]; then
      printf '0 1\n'
      exit 0
    fi
    exit 1
    ;;
  ls-remote)
    exit 1
    ;;
  *)
    echo "unexpected git command: $cmd $*" >&2
    exit 1
    ;;
esac
`, stateDir))

	writeExe(t, filepath.Join(binDir, "gh"), fmt.Sprintf(`#!/usr/bin/env bash
set -eu
state_dir=%q
cmd="$1"
shift

case "$cmd" in
  api)
    if [ "$1" = "user" ]; then
      printf '{"login":"rascalbot"}\n'
      exit 0
    fi
    ;;
  pr)
    sub="$1"
    shift
    case "$sub" in
      view)
        if [ -f "$state_dir/pr_created" ]; then
          printf '{"number":77,"url":"https://github.com/owner/repo/pull/77"}\n'
          exit 0
        fi
        exit 1
        ;;
      create)
        has_label=false
        while [ "$#" -gt 0 ]; do
          if [ "$1" = "--label" ] && [ "$#" -ge 2 ] && [ "$2" = "rascal" ]; then
            has_label=true
            break
          fi
          shift
        done
        if [ "$has_label" != true ]; then
          echo "expected gh pr create to include --label rascal" >&2
          exit 1
        fi
        : > "$state_dir/pr_created"
        printf 'https://github.com/owner/repo/pull/77\n'
        exit 0
        ;;
    esac
    ;;
esac

echo "unexpected gh command: $cmd $*" >&2
exit 1
`, stateDir))

	writeExe(t, filepath.Join(binDir, "goose"), `#!/usr/bin/env bash
set -eu
printf '{"event":"message","usage":{"total_tokens":321}}'"\n"
`)

	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("RASCAL_RUN_ID", "run_fake")
	t.Setenv("RASCAL_TASK_ID", "task_fake")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_INSTRUCTION", "Address feedback")
	t.Setenv("RASCAL_AGENT_RUNTIME", "goose")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)

	if err := worker.Run(); err != nil {
		t.Fatalf("run returned error: %v", err)
	}

	metaPath := filepath.Join(metaDir, "meta.json")
	metaData, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta struct {
		BuildCommit string `json:"build_commit"`
		ExitCode    int    `json:"exit_code"`
		PRNumber    int    `json:"pr_number"`
		PRURL       string `json:"pr_url"`
		HeadSHA     string `json:"head_sha"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta.json: %v", err)
	}
	if meta.BuildCommit != "unknown" {
		t.Fatalf("unexpected build_commit: %q", meta.BuildCommit)
	}
	if meta.ExitCode != 0 {
		t.Fatalf("expected exit_code=0, got %d", meta.ExitCode)
	}
	if meta.PRNumber != 77 {
		t.Fatalf("expected pr_number=77, got %d", meta.PRNumber)
	}
	if meta.PRURL != "https://github.com/owner/repo/pull/77" {
		t.Fatalf("unexpected pr_url: %q", meta.PRURL)
	}
	if meta.HeadSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected head_sha: %q", meta.HeadSHA)
	}

	prBodyData, err := os.ReadFile(filepath.Join(metaDir, "pr_body.md"))
	if err != nil {
		t.Fatalf("read pr_body.md: %v", err)
	}
	prBody := string(prBodyData)
	if !strings.Contains(prBody, "<details><summary>Agent Details</summary>") {
		t.Fatalf("expected agent details block in pr body:\n%s", prBody)
	}
	if !strings.Contains(prBody, "Rascal run `run_fake` completed in ") || !strings.Contains(prBody, "· 321 tokens") {
		t.Fatalf("expected token summary in pr body:\n%s", prBody)
	}
}

func TestRunWithExecutorUsesCodexBackend(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	repoDir := filepath.Join(workRoot, "repo")
	codexSessionPath := filepath.Join(metaDir, "codex-home", "sessions", "2026", "03", "session.jsonl")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo git dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Join(metaDir, "codex"), 0o755); err != nil {
		t.Fatalf("mkdir codex auth dir: %v", err)
	}
	if err := os.MkdirAll(filepath.Dir(codexSessionPath), 0o755); err != nil {
		t.Fatalf("mkdir codex session dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(metaDir, "codex", "auth.json"), []byte(`{"token":"test"}`), 0o600); err != nil {
		t.Fatalf("write codex auth: %v", err)
	}

	t.Setenv("RASCAL_RUN_ID", "run_codex_executor")
	t.Setenv("RASCAL_TASK_ID", "task_codex_executor")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_INSTRUCTION", "Address Codex feedback")
	t.Setenv("RASCAL_AGENT_RUNTIME", "codex")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)
	t.Setenv("CODEX_HOME", filepath.Join(metaDir, "codex-home"))

	var ranCodex bool
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			switch {
			case name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user":
				return `{"login":"rascalbot"}`, nil
			case name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view":
				return `{"number":88,"url":"https://github.com/owner/repo/pull/88"}`, nil
			case name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain":
				return " M changed.txt\n", nil
			case name == "git" && len(args) >= 4 && args[0] == "rev-list" && args[1] == "--left-right" && args[2] == "--count":
				return "0 1", nil
			case name == "git" && len(args) >= 2 && args[0] == "rev-parse" && args[1] == "HEAD":
				return "0123456789abcdef0123456789abcdef01234567", nil
			default:
				return "", nil
			}
		},
		runWithInputFn: func(_ string, _ []string, stdin io.Reader, stdout, _ io.Writer, name string, args ...string) error {
			if name != "codex" {
				t.Fatalf("unexpected command: %s", name)
			}
			ranCodex = true
			input, err := io.ReadAll(stdin)
			if err != nil {
				t.Fatalf("read codex stdin: %v", err)
			}
			if !strings.Contains(string(input), "Rascal Instructions") {
				t.Fatalf("expected instructions on stdin, got %q", string(input))
			}
			if err := os.WriteFile(filepath.Join(metaDir, "agent_output.txt"), []byte("final codex response"), 0o644); err != nil {
				t.Fatalf("write codex output: %v", err)
			}
			if err := os.WriteFile(codexSessionPath, []byte(`{"type":"session_meta","payload":{"id":"session-codex"}}`+"\n"), 0o644); err != nil {
				t.Fatalf("write codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex log: %w", err)
			}
			return nil
		},
	}

	if err := worker.RunWithExecutor(ex); err != nil {
		t.Fatalf("runWithExecutor returned error: %v", err)
	}
	if !ranCodex {
		t.Fatal("expected codex command to run")
	}

	metaData, err := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if err != nil {
		t.Fatalf("read meta.json: %v", err)
	}
	var meta struct {
		ExitCode      int    `json:"exit_code"`
		PRNumber      int    `json:"pr_number"`
		PRURL         string `json:"pr_url"`
		HeadSHA       string `json:"head_sha"`
		TaskSessionID string `json:"task_session_id"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta.json: %v", err)
	}
	if meta.ExitCode != 0 {
		t.Fatalf("expected exit_code=0, got %d", meta.ExitCode)
	}
	if meta.PRNumber != 88 {
		t.Fatalf("expected pr_number=88, got %d", meta.PRNumber)
	}
	if meta.PRURL != "https://github.com/owner/repo/pull/88" {
		t.Fatalf("unexpected pr_url: %q", meta.PRURL)
	}
	if meta.HeadSHA != "0123456789abcdef0123456789abcdef01234567" {
		t.Fatalf("unexpected head_sha: %q", meta.HeadSHA)
	}
	if meta.TaskSessionID != "session-codex" {
		t.Fatalf("unexpected task session id: %q", meta.TaskSessionID)
	}
}

func TestRunWithExecutorFailsWhenRequiredCommandMissing(t *testing.T) {
	tests := []struct {
		name           string
		backend        string
		missingCommand string
	}{
		{name: "goose", backend: "goose", missingCommand: "goose"},
		{name: "codex", backend: "codex", missingCommand: "codex"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			metaDir := filepath.Join(t.TempDir(), "meta")
			workRoot := filepath.Join(t.TempDir(), "work")
			if err := os.MkdirAll(metaDir, 0o755); err != nil {
				t.Fatalf("mkdir meta dir: %v", err)
			}
			if err := os.MkdirAll(workRoot, 0o755); err != nil {
				t.Fatalf("mkdir work dir: %v", err)
			}

			t.Setenv("RASCAL_RUN_ID", "run_missing_cmd_"+tc.name)
			t.Setenv("RASCAL_TASK_ID", "task_missing_cmd_"+tc.name)
			t.Setenv("RASCAL_REPO", "owner/repo")
			t.Setenv("GH_TOKEN", "token")
			t.Setenv("RASCAL_AGENT_RUNTIME", tc.backend)
			t.Setenv("RASCAL_META_DIR", metaDir)
			t.Setenv("RASCAL_WORK_ROOT", workRoot)

			ex := fakeExecutor{
				lookPathFn: func(name string) error {
					if name == tc.missingCommand {
						return errors.New("missing")
					}
					return nil
				},
			}
			err := worker.RunWithExecutor(ex)
			expected := "stage validate_commands: required command missing: " + tc.missingCommand
			if err == nil || !strings.Contains(err.Error(), expected) {
				t.Fatalf("expected %q, got: %v", expected, err)
			}

			metaData, readErr := os.ReadFile(filepath.Join(metaDir, "meta.json"))
			if readErr != nil {
				t.Fatalf("read meta.json: %v", readErr)
			}
			var meta struct {
				ExitCode int    `json:"exit_code"`
				Error    string `json:"error"`
			}
			if err := json.Unmarshal(metaData, &meta); err != nil {
				t.Fatalf("decode meta: %v", err)
			}
			if meta.ExitCode == 0 {
				t.Fatalf("expected non-zero exit code in meta, got %d", meta.ExitCode)
			}
			if !strings.Contains(meta.Error, expected) {
				t.Fatalf("expected missing command in meta error, got %q", meta.Error)
			}
		})
	}
}

func TestRunWithExecutorSetsMetaErrorOnPRCreateFailure(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	repoDir := filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo git dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}

	t.Setenv("RASCAL_RUN_ID", "run_pr_create_fail")
	t.Setenv("RASCAL_TASK_ID", "task_pr_create_fail")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_INSTRUCTION", "Address PR feedback")
	t.Setenv("RASCAL_AGENT_RUNTIME", "goose")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)

	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return "", errors.New("not found")
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "create" {
				return "", errors.New("create failed")
			}
			if name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
				return " M changed.txt\n", nil
			}
			if name == "git" && len(args) >= 4 && args[0] == "rev-list" && args[1] == "--left-right" && args[2] == "--count" {
				return "0 1", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, _ ...string) error {
			if name == "goose" {
				if _, err := io.WriteString(stdout, `{"event":"message","usage":{"total_tokens":7}}`+"\n"); err != nil {
					return fmt.Errorf("write fake goose output: %w", err)
				}
			}
			return nil
		},
	}

	err := worker.RunWithExecutor(ex)
	if err == nil || !strings.Contains(err.Error(), "stage pr_create: gh pr create failed") {
		t.Fatalf("expected pr create failure, got: %v", err)
	}

	metaData, readErr := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if readErr != nil {
		t.Fatalf("read meta.json: %v", readErr)
	}
	var meta struct {
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code in meta, got %d", meta.ExitCode)
	}
	if !strings.Contains(meta.Error, "stage pr_create: gh pr create failed") {
		t.Fatalf("expected gh pr create failure in meta error, got %q", meta.Error)
	}
}

func TestRunWithExecutorFailsWhenAgentProducesNoCommitsAheadOfBase(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	repoDir := filepath.Join(workRoot, "repo")
	if err := os.MkdirAll(filepath.Join(repoDir, ".git"), 0o755); err != nil {
		t.Fatalf("mkdir repo git dir: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}

	t.Setenv("RASCAL_RUN_ID", "run_no_branch_diff")
	t.Setenv("RASCAL_TASK_ID", "task_no_branch_diff")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_INSTRUCTION", "Address PR feedback")
	t.Setenv("RASCAL_AGENT_RUNTIME", "goose")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)

	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "gh" && len(args) >= 2 && args[0] == "api" && args[1] == "user" {
				return `{"login":"rascalbot"}`, nil
			}
			if name == "gh" && len(args) >= 2 && args[0] == "pr" && args[1] == "view" {
				return "", errors.New("not found")
			}
			if name == "git" && len(args) >= 2 && args[0] == "status" && args[1] == "--porcelain" {
				return "", nil
			}
			if name == "git" && len(args) >= 4 && args[0] == "rev-list" && args[1] == "--left-right" && args[2] == "--count" {
				return "0 0", nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, _ ...string) error {
			if name == "goose" {
				if _, err := io.WriteString(stdout, `{"event":"message","usage":{"total_tokens":7}}`+"\n"); err != nil {
					return fmt.Errorf("write fake goose output: %w", err)
				}
			}
			return nil
		},
	}

	err := worker.RunWithExecutor(ex)
	if err == nil || !strings.Contains(err.Error(), "stage check_branch_diff: agent produced no commits ahead of main") {
		t.Fatalf("expected no-branch-diff failure, got: %v", err)
	}

	metaData, readErr := os.ReadFile(filepath.Join(metaDir, "meta.json"))
	if readErr != nil {
		t.Fatalf("read meta.json: %v", readErr)
	}
	var meta struct {
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.ExitCode == 0 {
		t.Fatalf("expected non-zero exit code in meta, got %d", meta.ExitCode)
	}
	if !strings.Contains(meta.Error, "stage check_branch_diff: agent produced no commits ahead of main") {
		t.Fatalf("expected branch diff failure in meta error, got %q", meta.Error)
	}
}

func TestRunStageWrapsError(t *testing.T) {
	err := worker.RunStage("checkout_repo", func() error {
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected runStage error")
	}
	if !strings.Contains(err.Error(), "stage checkout_repo: boom") {
		t.Fatalf("unexpected wrapped error: %v", err)
	}

	if err := worker.RunStage("ok_stage", func() error { return nil }); err != nil {
		t.Fatalf("expected nil error on success stage, got %v", err)
	}
}

func writeExe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
