package main

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/worker"
)

func TestTaskSubject(t *testing.T) {
	t.Run("uses fallback when task is empty", func(t *testing.T) {
		got := worker.TaskSubject("   ", "task_1")
		if got != "task_1" {
			t.Fatalf("taskSubject fallback = %q, want task_1", got)
		}
	})

	t.Run("collapses whitespace", func(t *testing.T) {
		got := worker.TaskSubject("fix\n  spacing\tplease", "task_1")
		if got != "fix spacing please" {
			t.Fatalf("taskSubject normalized = %q", got)
		}
	})

	t.Run("truncates long subject", func(t *testing.T) {
		got := worker.TaskSubject(strings.Repeat("a", 70), "task_1")
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
		if !worker.IsConventionalTitle(title) {
			t.Fatalf("expected valid conventional title: %q", title)
		}
	}

	invalid := []string{
		"update runner",
		"Feat: wrong case prefix",
		"",
	}
	for _, title := range invalid {
		if worker.IsConventionalTitle(title) {
			t.Fatalf("expected invalid conventional title: %q", title)
		}
	}
}

func TestBuildInfoSummary(t *testing.T) {
	origVersion, origCommit, origTime := worker.BuildVersion, worker.BuildCommit, worker.BuildTime
	t.Cleanup(func() {
		worker.BuildVersion, worker.BuildCommit, worker.BuildTime = origVersion, origCommit, origTime
	})

	worker.BuildVersion = "v1.2.3"
	worker.BuildCommit = "abcdef0"
	worker.BuildTime = "2026-03-03T12:00:00Z"
	got := worker.BuildInfoSummary()
	want := "version=v1.2.3 commit=abcdef0 built=2026-03-03T12:00:00Z"
	if got != want {
		t.Fatalf("worker.BuildInfoSummary() = %q, want %q", got, want)
	}
}

func TestLoadAgentCommitMessage(t *testing.T) {
	t.Run("returns empty when file missing", func(t *testing.T) {
		title, body, err := worker.LoadAgentCommitMessage(filepath.Join(t.TempDir(), "missing.txt"))
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
		title, body, err := worker.LoadAgentCommitMessage(path)
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

	cfg := worker.Config{
		RepoDir:       repoDir,
		CommitMsgPath: filepath.Join(metaDir, "commit_message.txt"),
	}
	if err := worker.NormalizeRepoLocalMetaArtifacts(cfg); err != nil {
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

	cfg := worker.Config{
		RepoDir:       repoDir,
		CommitMsgPath: canonical,
	}
	if err := worker.NormalizeRepoLocalMetaArtifacts(cfg); err != nil {
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
