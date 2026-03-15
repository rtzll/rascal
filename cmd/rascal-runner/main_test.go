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

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtrigger"
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

func TestTaskSubject(t *testing.T) {
	t.Run("uses fallback when task is empty", func(t *testing.T) {
		got := taskSubject("   ", "task_1")
		if got != "task_1" {
			t.Fatalf("taskSubject fallback = %q, want task_1", got)
		}
	})

	t.Run("collapses whitespace", func(t *testing.T) {
		got := taskSubject("fix\n  spacing\tplease", "task_1")
		if got != "fix spacing please" {
			t.Fatalf("taskSubject normalized = %q", got)
		}
	})

	t.Run("truncates long subject", func(t *testing.T) {
		got := taskSubject(strings.Repeat("a", 70), "task_1")
		if len(got) != 58 {
			t.Fatalf("expected length 58, got %d", len(got))
		}
		if !strings.HasSuffix(got, "...") {
			t.Fatalf("expected ellipsis suffix, got %q", got)
		}
	})
}

func TestIsConventionalTitle(t *testing.T) {
	valid := []string{
		"feat(rascal): add runner binary",
		"fix: handle missing env",
		"chore(ci)!: switch image build",
	}
	for _, title := range valid {
		if !isConventionalTitle(title) {
			t.Fatalf("expected valid conventional title: %q", title)
		}
	}

	invalid := []string{
		"update runner",
		"Feat: wrong case prefix",
		"",
	}
	for _, title := range invalid {
		if isConventionalTitle(title) {
			t.Fatalf("expected invalid conventional title: %q", title)
		}
	}
}

func TestBuildInfoSummary(t *testing.T) {
	origVersion, origCommit, origTime := buildVersion, buildCommit, buildTime
	t.Cleanup(func() {
		buildVersion, buildCommit, buildTime = origVersion, origCommit, origTime
	})

	buildVersion = "v1.2.3"
	buildCommit = "abcdef0"
	buildTime = "2026-03-03T12:00:00Z"
	got := buildInfoSummary()
	want := "version=v1.2.3 commit=abcdef0 built=2026-03-03T12:00:00Z"
	if got != want {
		t.Fatalf("buildInfoSummary() = %q, want %q", got, want)
	}
}

func TestLoadAgentCommitMessage(t *testing.T) {
	t.Run("returns empty when file missing", func(t *testing.T) {
		title, body, err := loadAgentCommitMessage(filepath.Join(t.TempDir(), "missing.txt"))
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if title != "" || body != "" {
			t.Fatalf("expected empty title/body, got %q / %q", title, body)
		}
	})

	t.Run("parses title and body", func(t *testing.T) {
		path := filepath.Join(t.TempDir(), "commit_message.txt")
		content := "feat(rascal): runner binary\n\n- move entrypoint logic\n- add tests\n"
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("write file: %v", err)
		}
		title, body, err := loadAgentCommitMessage(path)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if title != "feat(rascal): runner binary" {
			t.Fatalf("unexpected title: %q", title)
		}
		wantBody := "- move entrypoint logic\n- add tests"
		if body != wantBody {
			t.Fatalf("unexpected body: got %q want %q", body, wantBody)
		}
	})
}

func TestNormalizeRepoLocalMetaArtifactsAdoptsCommitMessage(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	metaDir := filepath.Join(t.TempDir(), "meta")
	if err := os.MkdirAll(filepath.Join(repoDir, "rascal-meta"), 0o755); err != nil {
		t.Fatalf("mkdir repo-local meta: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}

	repoLocalCommit := filepath.Join(repoDir, "rascal-meta", "commit_message.txt")
	want := "fix(runner): keep commit message out of repo\n"
	if err := os.WriteFile(repoLocalCommit, []byte(want), 0o644); err != nil {
		t.Fatalf("write repo-local commit message: %v", err)
	}

	cfg := config{
		RepoDir:       repoDir,
		CommitMsgPath: filepath.Join(metaDir, "commit_message.txt"),
	}
	if err := normalizeRepoLocalMetaArtifacts(cfg); err != nil {
		t.Fatalf("normalize repo-local meta artifacts: %v", err)
	}

	got, err := os.ReadFile(cfg.CommitMsgPath)
	if err != nil {
		t.Fatalf("read adopted commit message: %v", err)
	}
	if string(got) != want {
		t.Fatalf("commit message = %q, want %q", string(got), want)
	}
	if _, err := os.Stat(filepath.Join(repoDir, "rascal-meta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected repo-local rascal-meta removed, got err=%v", err)
	}
}

func TestNormalizeRepoLocalMetaArtifactsPreservesCanonicalCommitMessage(t *testing.T) {
	repoDir := filepath.Join(t.TempDir(), "repo")
	metaDir := filepath.Join(t.TempDir(), "meta")
	if err := os.MkdirAll(filepath.Join(repoDir, "rascal-meta"), 0o755); err != nil {
		t.Fatalf("mkdir repo-local meta: %v", err)
	}
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}

	canonical := filepath.Join(metaDir, "commit_message.txt")
	if err := os.WriteFile(canonical, []byte("feat(runner): canonical\n"), 0o644); err != nil {
		t.Fatalf("write canonical commit message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(repoDir, "rascal-meta", "commit_message.txt"), []byte("feat(runner): repo-local\n"), 0o644); err != nil {
		t.Fatalf("write repo-local commit message: %v", err)
	}

	cfg := config{
		RepoDir:       repoDir,
		CommitMsgPath: canonical,
	}
	if err := normalizeRepoLocalMetaArtifacts(cfg); err != nil {
		t.Fatalf("normalize repo-local meta artifacts: %v", err)
	}

	got, err := os.ReadFile(canonical)
	if err != nil {
		t.Fatalf("read canonical commit message: %v", err)
	}
	if string(got) != "feat(runner): canonical\n" {
		t.Fatalf("canonical commit message = %q", string(got))
	}
	if _, err := os.Stat(filepath.Join(repoDir, "rascal-meta")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected repo-local rascal-meta removed, got err=%v", err)
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_1")
	t.Setenv("RASCAL_TASK_ID", "task_1")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_TASK", "Do thing")
	t.Setenv("RASCAL_ISSUE_NUMBER", "7")
	t.Setenv("RASCAL_BASE_BRANCH", "")
	t.Setenv("RASCAL_HEAD_BRANCH", "")
	t.Setenv("RASCAL_TRIGGER", "")
	t.Setenv("GOOSE_PATH_ROOT", "")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_AGENT_SESSION_MODE", "")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_AGENT_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "")
	t.Setenv("RASCAL_AGENT_SESSION_KEY", "")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "")
	t.Setenv("RASCAL_AGENT_SESSION_ID", "")
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.BaseBranch != "main" {
		t.Fatalf("expected default base branch main, got %q", cfg.BaseBranch)
	}
	if cfg.HeadBranch != "rascal/run_1" {
		t.Fatalf("expected default head branch, got %q", cfg.HeadBranch)
	}
	if cfg.IssueNumber != 7 {
		t.Fatalf("expected issue number 7, got %d", cfg.IssueNumber)
	}
	if cfg.Trigger != runtrigger.NameCLI {
		t.Fatalf("expected default trigger cli, got %q", cfg.Trigger)
	}
	if cfg.AgentSession.Mode != agent.SessionModeOff {
		t.Fatalf("expected default agent session mode off, got %q", cfg.AgentSession.Mode)
	}
	if cfg.AgentSession.Resume {
		t.Fatal("expected default agent session resume to be false")
	}
	if cfg.AgentSession.BackendSessionID != "" {
		t.Fatalf("expected default agent session name empty, got %q", cfg.AgentSession.BackendSessionID)
	}
}

func TestLoadConfigRespectsDirectoryOverrides(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	repoDir := filepath.Join(t.TempDir(), "repo")
	t.Setenv("RASCAL_RUN_ID", "run_2")
	t.Setenv("RASCAL_TASK_ID", "task_2")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_BASE_BRANCH", "")
	t.Setenv("RASCAL_HEAD_BRANCH", "")
	t.Setenv("RASCAL_TRIGGER", "")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)
	t.Setenv("GOOSE_PATH_ROOT", "")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_AGENT_SESSION_MODE", "")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_AGENT_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "")
	t.Setenv("RASCAL_AGENT_SESSION_KEY", "")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "")
	t.Setenv("RASCAL_AGENT_SESSION_ID", "")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.MetaDir != metaDir {
		t.Fatalf("meta dir = %q, want %q", cfg.MetaDir, metaDir)
	}
	if cfg.WorkRoot != workRoot {
		t.Fatalf("work root = %q, want %q", cfg.WorkRoot, workRoot)
	}
	if cfg.RepoDir != repoDir {
		t.Fatalf("repo dir = %q, want %q", cfg.RepoDir, repoDir)
	}
	if cfg.GoosePathRoot != filepath.Join(metaDir, "goose") {
		t.Fatalf("goose path root = %q, want %q", cfg.GoosePathRoot, filepath.Join(metaDir, "goose"))
	}
}

func TestLoadConfigRespectsGooseSessionEnv(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	t.Setenv("RASCAL_RUN_ID", "run_3")
	t.Setenv("RASCAL_TASK_ID", "owner/repo#3")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "pr-only")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "true")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "owner-repo-3-abc123")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "rascal-owner-repo-3-abc123")
	t.Setenv("GOOSE_PATH_ROOT", "/rascal-goose-session")

	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.AgentSession.Mode != agent.SessionModePROnly {
		t.Fatalf("AgentSession.Mode = %q, want %q", cfg.AgentSession.Mode, agent.SessionModePROnly)
	}
	if !cfg.AgentSession.Resume {
		t.Fatal("AgentSession.Resume should be true")
	}
	if cfg.AgentSession.TaskKey != "owner-repo-3-abc123" {
		t.Fatalf("AgentSession.TaskKey = %q, want owner-repo-3-abc123", cfg.AgentSession.TaskKey)
	}
	if cfg.AgentSession.BackendSessionID != "rascal-owner-repo-3-abc123" {
		t.Fatalf("AgentSession.BackendSessionID = %q, want rascal-owner-repo-3-abc123", cfg.AgentSession.BackendSessionID)
	}
	if cfg.GoosePathRoot != "/rascal-goose-session" {
		t.Fatalf("GoosePathRoot = %q, want /rascal-goose-session", cfg.GoosePathRoot)
	}
}

func TestLoadConfigRejectsInvalidAgentBackend(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_invalid_backend")
	t.Setenv("RASCAL_TASK_ID", "task_invalid_backend")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_AGENT_BACKEND", "claude")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid RASCAL_AGENT_BACKEND") {
		t.Fatalf("loadConfig error = %q", err.Error())
	}
}

func TestLoadConfigRejectsInvalidAgentSessionMode(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_invalid_session_mode")
	t.Setenv("RASCAL_TASK_ID", "task_invalid_session_mode")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_AGENT_SESSION_MODE", "sometimes")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_AGENT_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "")
	t.Setenv("RASCAL_AGENT_SESSION_KEY", "")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "")
	t.Setenv("RASCAL_AGENT_SESSION_ID", "")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid agent session mode") {
		t.Fatalf("loadConfig error = %q", err.Error())
	}
}

func TestLoadConfigRejectsInvalidTrigger(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_invalid_trigger")
	t.Setenv("RASCAL_TASK_ID", "task_invalid_trigger")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_TRIGGER", "issue")

	_, err := loadConfig()
	if err == nil {
		t.Fatal("loadConfig error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid RASCAL_TRIGGER") {
		t.Fatalf("loadConfig error = %q", err.Error())
	}
}

func TestRunGooseNoSessionByDefault(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		AgentSession:     runner.SessionSpec{Mode: agent.SessionModeOff},
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

	if _, _, err := runGoose(ex, cfg); err != nil {
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
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			BackendSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var gotArgs []string
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return fmt.Sprintf(`[{"name":%q}]`, cfg.AgentSession.BackendSessionID), nil
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

	if _, _, err := runGoose(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"--name", cfg.AgentSession.BackendSessionID, "--resume"} {
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
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-missing",
			BackendSessionID: "rascal-owner-repo-task-missing",
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

	if _, _, err := runGoose(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	if strings.Contains(argsText, "--resume") {
		t.Fatalf("did not expect --resume args when session is missing, got %q", argsText)
	}
	if !strings.Contains(argsText, "--name "+cfg.AgentSession.BackendSessionID) {
		t.Fatalf("expected named fresh session args, got %q", argsText)
	}
}

func TestRunGooseFallsBackToFreshSessionOnResumeStateError(t *testing.T) {
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "goose-sessions")
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    sessionRoot,
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			BackendSessionID: "rascal-owner-repo-task-abc123",
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
				return fmt.Sprintf(`[{"name":%q}]`, cfg.AgentSession.BackendSessionID), nil
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

	if _, _, err := runGoose(ex, cfg); err != nil {
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

func TestResetGooseSessionRootCreatesRootWhenMissing(t *testing.T) {
	root := filepath.Join(t.TempDir(), "missing-goose-root")
	if err := resetGooseSessionRoot(root); err != nil {
		t.Fatalf("resetGooseSessionRoot returned error: %v", err)
	}
	info, err := os.Stat(root)
	if err != nil {
		t.Fatalf("stat root after reset: %v", err)
	}
	if !info.IsDir() {
		t.Fatalf("expected %s to be a directory", root)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read root after reset: %v", err)
	}
	if len(entries) != 0 {
		t.Fatalf("expected empty root after reset, found %d entries", len(entries))
	}
}

func TestRunGooseDoesNotFallbackOnUnrelatedFailure(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			BackendSessionID: "rascal-owner-repo-task-abc123",
		},
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	calls := 0
	ex := fakeExecutor{
		combinedFn: func(_ string, _ []string, name string, args ...string) (string, error) {
			if name == "goose" && len(args) == 4 && args[0] == "session" && args[1] == "list" && args[2] == "--format" && args[3] == "json" {
				return fmt.Sprintf(`[{"name":%q}]`, cfg.AgentSession.BackendSessionID), nil
			}
			return "", nil
		},
		runFn: func(_ string, _ []string, _ io.Writer, _ io.Writer, _ string, _ ...string) error {
			calls++
			return errors.New("goose transport timeout")
		},
	}

	_, _, err := runGoose(ex, cfg)
	if err == nil {
		t.Fatal("expected runGoose to fail")
	}
	if calls != 1 {
		t.Fatalf("expected one goose attempt, got %d", calls)
	}
}

func TestIsSessionResumeFailureDetectsMissingNamedSession(t *testing.T) {
	root := t.TempDir()
	logPath := filepath.Join(root, "agent.ndjson")
	if err := os.WriteFile(logPath, []byte("Error: No session found with name 'rascal-owner-repo-task-abc123'\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	if !isSessionResumeFailure(errors.New("exit status 1"), logPath) {
		t.Fatal("expected missing named session to trigger resume fallback detection")
	}
}

func TestRunGooseKeepsResumeWhenSessionPreflightFails(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		GooseDebug:       false,
		GoosePathRoot:    filepath.Join(root, "goose-sessions"),
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModePROnly,
			Resume:           true,
			TaskKey:          "owner-repo-task-abc123",
			BackendSessionID: "rascal-owner-repo-task-abc123",
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

	if _, _, err := runGoose(ex, cfg); err != nil {
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
	cfg := config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentBackend:     agent.BackendCodex,
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
			if err := os.WriteFile(sessionPath, []byte(`{"type":"session_meta","payload":{"id":"session-123"}}`+"\n"), 0o644); err != nil {
				t.Fatalf("write codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	output, sessionID, err := runCodex(ex, cfg)
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
	for _, want := range []string{"exec", "--json", "--full-auto", "--skip-git-repo-check", "-s", "workspace-write", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, " resume ") {
		t.Fatalf("did not expect resume args in fresh codex run, got %q", argsText)
	}
	if _, err := os.Stat(filepath.Join(codexHome, "auth.json")); err != nil {
		t.Fatalf("expected codex auth copied into home: %v", err)
	}
}

func TestRunCodexResumeSession(t *testing.T) {
	root := t.TempDir()
	codexHome := filepath.Join(root, "codex-home")
	sessionPath := filepath.Join(codexHome, "sessions", "2026", "03", "session.jsonl")
	cfg := config{
		RepoDir:          root,
		MetaDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "agent.ndjson"),
		AgentOutputPath:  filepath.Join(root, "agent_output.txt"),
		CodexHome:        codexHome,
		AgentBackend:     agent.BackendCodex,
		AgentSession: runner.SessionSpec{
			Mode:             agent.SessionModeAll,
			Resume:           true,
			BackendSessionID: "session-abc",
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
			if err := os.WriteFile(sessionPath, []byte(`{"type":"session_meta","payload":{"id":"session-abc"}}`+"\n"), 0o644); err != nil {
				t.Fatalf("write codex session: %v", err)
			}
			if _, err := io.WriteString(stdout, `{"type":"message"}`+"\n"); err != nil {
				return fmt.Errorf("write fake codex output: %w", err)
			}
			return nil
		},
	}

	_, sessionID, err := runCodex(ex, cfg)
	if err != nil {
		t.Fatalf("runCodex returned error: %v", err)
	}
	if sessionID != "session-abc" {
		t.Fatalf("sessionID = %q, want session-abc", sessionID)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"exec", "resume", "--json", "--full-auto", "--skip-git-repo-check", "session-abc", "-"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "workspace-write") {
		t.Fatalf("did not expect explicit sandbox arg on resume, got %q", argsText)
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
	t.Setenv("RASCAL_TASK", "Address feedback")
	t.Setenv("RASCAL_AGENT_BACKEND", "goose")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)

	if err := run(); err != nil {
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
	t.Setenv("RASCAL_TASK", "Address Codex feedback")
	t.Setenv("RASCAL_AGENT_BACKEND", "codex")
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

	if err := runWithExecutor(ex); err != nil {
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
		ExitCode       int    `json:"exit_code"`
		PRNumber       int    `json:"pr_number"`
		PRURL          string `json:"pr_url"`
		HeadSHA        string `json:"head_sha"`
		AgentSessionID string `json:"agent_session_id"`
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
	if meta.AgentSessionID != "session-codex" {
		t.Fatalf("unexpected agent session id: %q", meta.AgentSessionID)
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
			t.Setenv("RASCAL_AGENT_BACKEND", tc.backend)
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
			err := runWithExecutor(ex)
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
	t.Setenv("RASCAL_TASK", "Address PR feedback")
	t.Setenv("RASCAL_AGENT_BACKEND", "goose")
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

	err := runWithExecutor(ex)
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

func TestRunStageWrapsError(t *testing.T) {
	err := runStage("checkout_repo", func() error {
		return errors.New("boom")
	})
	if err == nil {
		t.Fatal("expected runStage error")
	}
	if !strings.Contains(err.Error(), "stage checkout_repo: boom") {
		t.Fatalf("unexpected wrapped error: %v", err)
	}

	if err := runStage("ok_stage", func() error { return nil }); err != nil {
		t.Fatalf("expected nil error on success stage, got %v", err)
	}
}

func writeExe(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatalf("write executable %s: %v", path, err)
	}
}
