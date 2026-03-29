package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/worker"
)

func TestLoadConfig(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_1")
	t.Setenv("RASCAL_TASK_ID", "task_1")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_INSTRUCTION", "Do thing")
	t.Setenv("RASCAL_ISSUE_NUMBER", "7")
	t.Setenv("RASCAL_BASE_BRANCH", "")
	t.Setenv("RASCAL_HEAD_BRANCH", "")
	t.Setenv("RASCAL_TRIGGER", "")
	t.Setenv("GOOSE_PATH_ROOT", "")
	t.Setenv("RASCAL_TASK_SESSION_MODE", "")
	t.Setenv("RASCAL_TASK_SESSION_RESUME", "")
	t.Setenv("RASCAL_TASK_SESSION_KEY", "")
	t.Setenv("RASCAL_TASK_SESSION_ID", "")
	cfg, err := worker.LoadConfig()
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
	if cfg.TaskSession.Mode != runtime.SessionModeOff {
		t.Fatalf("expected default agent session mode off, got %q", cfg.TaskSession.Mode)
	}
	if cfg.TaskSession.Resume {
		t.Fatal("expected default agent session resume to be false")
	}
	if cfg.TaskSession.RuntimeSessionID != "" {
		t.Fatalf("expected default agent session name empty, got %q", cfg.TaskSession.RuntimeSessionID)
	}
	if cfg.PersistentInstructionsPath != filepath.Join("/rascal-meta", "persistent_instructions.md") {
		t.Fatalf("expected default persistent instructions path, got %q", cfg.PersistentInstructionsPath)
	}
}

func TestLoadConfigReadsGitHubTokenFromFile(t *testing.T) {
	tokenFile := filepath.Join(t.TempDir(), "gh_token")
	if err := os.WriteFile(tokenFile, []byte("token-from-file\n"), 0o600); err != nil {
		t.Fatalf("write token file: %v", err)
	}
	t.Setenv("RASCAL_RUN_ID", "run_file")
	t.Setenv("RASCAL_TASK_ID", "task_file")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "")
	t.Setenv("GH_TOKEN_FILE", tokenFile)

	cfg, err := worker.LoadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.GitHubToken != "token-from-file" {
		t.Fatalf("GitHubToken = %q, want token-from-file", cfg.GitHubToken)
	}
	if cfg.GitHubTokenFile != tokenFile {
		t.Fatalf("GitHubTokenFile = %q, want %q", cfg.GitHubTokenFile, tokenFile)
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
	t.Setenv("RASCAL_TASK_SESSION_MODE", "")
	t.Setenv("RASCAL_TASK_SESSION_RESUME", "")
	t.Setenv("RASCAL_TASK_SESSION_KEY", "")
	t.Setenv("RASCAL_TASK_SESSION_ID", "")

	cfg, err := worker.LoadConfig()
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
	if cfg.PersistentInstructionsPath != filepath.Join(metaDir, "persistent_instructions.md") {
		t.Fatalf("persistent instructions path = %q, want %q", cfg.PersistentInstructionsPath, filepath.Join(metaDir, "persistent_instructions.md"))
	}
}

func TestLoadConfigRespectsTaskSessionEnv(t *testing.T) {
	metaDir := filepath.Join(t.TempDir(), "meta")
	workRoot := filepath.Join(t.TempDir(), "work")
	t.Setenv("RASCAL_RUN_ID", "run_3")
	t.Setenv("RASCAL_TASK_ID", "owner/repo#3")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_META_DIR", metaDir)
	t.Setenv("RASCAL_WORK_ROOT", workRoot)
	t.Setenv("RASCAL_TASK_SESSION_MODE", "pr-only")
	t.Setenv("RASCAL_TASK_SESSION_RESUME", "true")
	t.Setenv("RASCAL_TASK_SESSION_KEY", "owner-repo-3-abc123")
	t.Setenv("RASCAL_TASK_SESSION_ID", "rascal-owner-repo-3-abc123")
	t.Setenv("GOOSE_PATH_ROOT", "/rascal-goose-session")

	cfg, err := worker.LoadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.TaskSession.Mode != runtime.SessionModePROnly {
		t.Fatalf("TaskSession.Mode = %q, want %q", cfg.TaskSession.Mode, runtime.SessionModePROnly)
	}
	if !cfg.TaskSession.Resume {
		t.Fatal("TaskSession.Resume should be true")
	}
	if cfg.TaskSession.TaskKey != "owner-repo-3-abc123" {
		t.Fatalf("TaskSession.TaskKey = %q, want owner-repo-3-abc123", cfg.TaskSession.TaskKey)
	}
	if cfg.TaskSession.RuntimeSessionID != "rascal-owner-repo-3-abc123" {
		t.Fatalf("TaskSession.RuntimeSessionID = %q, want rascal-owner-repo-3-abc123", cfg.TaskSession.RuntimeSessionID)
	}
	if cfg.GoosePathRoot != "/rascal-goose-session" {
		t.Fatalf("GoosePathRoot = %q, want /rascal-goose-session", cfg.GoosePathRoot)
	}
}

func TestLoadConfigRespectsPersistentInstructionsOverride(t *testing.T) {
	overridePath := filepath.Join(t.TempDir(), "custom-persistent.md")
	t.Setenv("RASCAL_RUN_ID", "run_persistent_override")
	t.Setenv("RASCAL_TASK_ID", "task_persistent_override")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("GOOSE_MOIM_MESSAGE_FILE", overridePath)

	cfg, err := worker.LoadConfig()
	if err != nil {
		t.Fatalf("loadConfig returned error: %v", err)
	}
	if cfg.PersistentInstructionsPath != overridePath {
		t.Fatalf("PersistentInstructionsPath = %q, want %q", cfg.PersistentInstructionsPath, overridePath)
	}
}

func TestLoadConfigRejectsInvalidAgentRuntime(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_invalid_backend")
	t.Setenv("RASCAL_TASK_ID", "task_invalid_backend")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_AGENT_RUNTIME", "unknown-agent")

	_, err := worker.LoadConfig()
	if err == nil {
		t.Fatal("loadConfig error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid RASCAL_AGENT_RUNTIME") {
		t.Fatalf("loadConfig error = %q", err.Error())
	}
}

func TestLoadConfigRejectsInvalidAgentSessionMode(t *testing.T) {
	t.Setenv("RASCAL_RUN_ID", "run_invalid_session_mode")
	t.Setenv("RASCAL_TASK_ID", "task_invalid_session_mode")
	t.Setenv("RASCAL_REPO", "owner/repo")
	t.Setenv("GH_TOKEN", "token")
	t.Setenv("RASCAL_TASK_SESSION_MODE", "sometimes")

	_, err := worker.LoadConfig()
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

	_, err := worker.LoadConfig()
	if err == nil {
		t.Fatal("loadConfig error = nil, want error")
	}
	if !strings.Contains(err.Error(), "invalid RASCAL_TRIGGER") {
		t.Fatalf("loadConfig error = %q", err.Error())
	}
}
