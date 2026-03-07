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
)

type fakeExecutor struct {
	lookPathFn func(name string) error
	combinedFn func(dir string, extraEnv []string, name string, args ...string) (string, error)
	runFn      func(dir string, extraEnv []string, stdout, stderr io.Writer, name string, args ...string) error
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
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "")
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
	if cfg.Trigger != "cli" {
		t.Fatalf("expected default trigger cli, got %q", cfg.Trigger)
	}
	if cfg.GooseSessionMode != runner.GooseSessionModeOff {
		t.Fatalf("expected default goose session mode off, got %q", cfg.GooseSessionMode)
	}
	if cfg.GooseSessionResume {
		t.Fatal("expected default goose session resume to be false")
	}
	if cfg.GooseSessionName != "" {
		t.Fatalf("expected default goose session name empty, got %q", cfg.GooseSessionName)
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
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_REPO_DIR", repoDir)
	t.Setenv("GOOSE_PATH_ROOT", "")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_GOOSE_SESSION_RESUME", "")
	t.Setenv("RASCAL_GOOSE_SESSION_KEY", "")
	t.Setenv("RASCAL_GOOSE_SESSION_NAME", "")

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
	if cfg.GooseSessionMode != runner.GooseSessionModePROnly {
		t.Fatalf("GooseSessionMode = %q, want %q", cfg.GooseSessionMode, runner.GooseSessionModePROnly)
	}
	if !cfg.GooseSessionResume {
		t.Fatal("GooseSessionResume should be true")
	}
	if cfg.GooseSessionKey != "owner-repo-3-abc123" {
		t.Fatalf("GooseSessionKey = %q, want owner-repo-3-abc123", cfg.GooseSessionKey)
	}
	if cfg.GooseSessionName != "rascal-owner-repo-3-abc123" {
		t.Fatalf("GooseSessionName = %q, want rascal-owner-repo-3-abc123", cfg.GooseSessionName)
	}
	if cfg.GoosePathRoot != "/rascal-goose-session" {
		t.Fatalf("GoosePathRoot = %q, want /rascal-goose-session", cfg.GoosePathRoot)
	}
}

func TestRunGooseNoSessionByDefault(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		RepoDir:          root,
		InstructionsPath: filepath.Join(root, "instructions.md"),
		GooseLogPath:     filepath.Join(root, "goose.ndjson"),
		GooseDebug:       false,
		GooseSessionMode: runner.GooseSessionModeOff,
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
			_, _ = io.WriteString(stdout, `{"event":"message"}`+"\n")
			return nil
		},
	}

	if _, err := runGoose(ex, cfg); err != nil {
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
		RepoDir:            root,
		InstructionsPath:   filepath.Join(root, "instructions.md"),
		GooseLogPath:       filepath.Join(root, "goose.ndjson"),
		GooseDebug:         false,
		GooseSessionMode:   runner.GooseSessionModePROnly,
		GooseSessionResume: true,
		GooseSessionName:   "rascal-owner-repo-task-abc123",
		GooseSessionKey:    "owner-repo-task-abc123",
		GoosePathRoot:      filepath.Join(root, "goose-sessions"),
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
			_, _ = io.WriteString(stdout, `{"event":"message"}`+"\n")
			return nil
		},
	}

	if _, err := runGoose(ex, cfg); err != nil {
		t.Fatalf("runGoose returned error: %v", err)
	}
	argsText := strings.Join(gotArgs, " ")
	for _, want := range []string{"--name", cfg.GooseSessionName, "--resume"} {
		if !strings.Contains(argsText, want) {
			t.Fatalf("expected %q in args, got %q", want, argsText)
		}
	}
	if strings.Contains(argsText, "--no-session") {
		t.Fatalf("did not expect --no-session args, got %q", argsText)
	}
}

func TestRunGooseFallsBackToFreshSessionOnResumeStateError(t *testing.T) {
	root := t.TempDir()
	sessionRoot := filepath.Join(root, "goose-sessions")
	cfg := config{
		RepoDir:            root,
		InstructionsPath:   filepath.Join(root, "instructions.md"),
		GooseLogPath:       filepath.Join(root, "goose.ndjson"),
		GooseDebug:         false,
		GooseSessionMode:   runner.GooseSessionModePROnly,
		GooseSessionResume: true,
		GooseSessionName:   "rascal-owner-repo-task-abc123",
		GooseSessionKey:    "owner-repo-task-abc123",
		GoosePathRoot:      sessionRoot,
	}
	if err := os.MkdirAll(sessionRoot, 0o755); err != nil {
		t.Fatalf("mkdir session root: %v", err)
	}
	if err := os.WriteFile(filepath.Join(sessionRoot, "stale.json"), []byte("bad"), 0o644); err != nil {
		t.Fatalf("write stale marker: %v", err)
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	var calls [][]string
	ex := fakeExecutor{
		runFn: func(_ string, _ []string, stdout, _ io.Writer, name string, args ...string) error {
			if name != "goose" {
				t.Fatalf("unexpected command: %s", name)
			}
			calls = append(calls, append([]string(nil), args...))
			if len(calls) == 1 {
				return errors.New("resume failed: session state missing")
			}
			_, _ = io.WriteString(stdout, `{"event":"message"}`+"\n")
			return nil
		},
	}

	if _, err := runGoose(ex, cfg); err != nil {
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
}

func TestRunGooseDoesNotFallbackOnUnrelatedFailure(t *testing.T) {
	root := t.TempDir()
	cfg := config{
		RepoDir:            root,
		InstructionsPath:   filepath.Join(root, "instructions.md"),
		GooseLogPath:       filepath.Join(root, "goose.ndjson"),
		GooseDebug:         false,
		GooseSessionMode:   runner.GooseSessionModePROnly,
		GooseSessionResume: true,
		GooseSessionName:   "rascal-owner-repo-task-abc123",
		GooseSessionKey:    "owner-repo-task-abc123",
		GoosePathRoot:      filepath.Join(root, "goose-sessions"),
	}
	if err := os.WriteFile(cfg.InstructionsPath, []byte("do thing"), 0o644); err != nil {
		t.Fatalf("write instructions: %v", err)
	}

	calls := 0
	ex := fakeExecutor{
		runFn: func(_ string, _ []string, _ io.Writer, _ io.Writer, _ string, _ ...string) error {
			calls++
			return errors.New("goose transport timeout")
		},
	}

	_, err := runGoose(ex, cfg)
	if err == nil {
		t.Fatal("expected runGoose to fail")
	}
	if calls != 1 {
		t.Fatalf("expected one goose attempt, got %d", calls)
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
		ExitCode int    `json:"exit_code"`
		PRNumber int    `json:"pr_number"`
		PRURL    string `json:"pr_url"`
		HeadSHA  string `json:"head_sha"`
	}
	if err := json.Unmarshal(metaData, &meta); err != nil {
		t.Fatalf("decode meta.json: %v", err)
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
	if !strings.Contains(prBody, "<details><summary>Goose Details</summary>") {
		t.Fatalf("expected goose details block in pr body:\n%s", prBody)
	}
	if !strings.Contains(prBody, "Rascal run `run_fake` completed in ") || !strings.Contains(prBody, "· 321 tokens") {
		t.Fatalf("expected token summary in pr body:\n%s", prBody)
	}
}

func TestRunWithExecutorFailsWhenRequiredCommandMissing(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	if err := os.MkdirAll(metaDir, 0o755); err != nil {
		t.Fatalf("mkdir meta dir: %v", err)
	}
	if err := os.MkdirAll(workRoot, 0o755); err != nil {
		t.Fatalf("mkdir work dir: %v", err)
	}

	t.Setenv("RASCAL_RUN_ID", "run_missing_cmd")
	t.Setenv("RASCAL_TASK_ID", "task_missing_cmd")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)

	ex := fakeExecutor{
		lookPathFn: func(name string) error {
			if name == "goose" {
				return errors.New("missing")
			}
			return nil
		},
	}
	err := runWithExecutor(ex)
	if err == nil || !strings.Contains(err.Error(), "stage validate_commands: required command missing: goose") {
		t.Fatalf("expected missing goose error, got: %v", err)
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
	if !strings.Contains(meta.Error, "stage validate_commands: required command missing: goose") {
		t.Fatalf("expected missing command in meta error, got %q", meta.Error)
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
				_, _ = io.WriteString(stdout, `{"event":"message","usage":{"total_tokens":7}}`+"\n")
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
