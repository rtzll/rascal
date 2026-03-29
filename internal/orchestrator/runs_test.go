package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/state"
)

func TestInstructionTextPRGitContext(t *testing.T) {
	run := state.Run{
		ID:          "run_abc123",
		TaskID:      "task_xyz789",
		Repo:        "acme/widgets",
		Instruction: "Address PR #137 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-xyz789",
		Trigger:     "pr_comment",
		IssueNumber: 42,
		PRNumber:    137,
		Context:     "Please rebase this on main and fix the conflicts.",
	}

	got := InstructionText(run)

	for _, want := range []string{
		"## Git Context",
		"- Remote: `origin`",
		"- Base branch: `main`",
		"- Head branch: `rascal/task-xyz789`",
		"- You may use `git` and `gh` directly.",
		"- Push only to `origin` branch `rascal/task-xyz789`.",
		"`git push --force-with-lease origin HEAD:rascal/task-xyz789`",
		"`git push origin HEAD:rascal/task-xyz789`",
		"do not rely on the harness to publish those changes for you",
		"## Additional Context",
		"Please rebase this on main and fix the conflicts.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("InstructionText() missing %q\nfull text:\n%s", want, got)
		}
	}
}

func TestInstructionTextNonPRRunOmitsGitContext(t *testing.T) {
	run := state.Run{
		ID:          "run_abc123",
		TaskID:      "task_xyz789",
		Repo:        "acme/widgets",
		Instruction: "Fix flaky test",
		BaseBranch:  "main",
		HeadBranch:  "rascal/fix-flaky-test",
		Trigger:     "issue",
	}

	got := InstructionText(run)
	if strings.Contains(got, "## Git Context") {
		t.Fatalf("InstructionText() unexpectedly included Git Context\nfull text:\n%s", got)
	}
}

func TestPersistentInstructionTextContainsDurableGuardrails(t *testing.T) {
	got := PersistentInstructionText(state.Run{})

	for _, want := range []string{
		"# Rascal Persistent Instructions",
		"Do not ask for interactive input.",
		"Do not overwrite, revert, or discard user changes you did not make unless the task explicitly requires it.",
		"Run `make lint` and `make test` before finishing if those targets exist.",
		"/rascal-meta/commit_message.txt",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("PersistentInstructionText() missing %q\nfull text:\n%s", want, got)
		}
	}
}

func TestWriteRunFilesWritesTypedContextJSON(t *testing.T) {
	runDir := t.TempDir()
	s := &Server{}
	run := state.Run{
		ID:          "run_abc123",
		TaskID:      "task_xyz789",
		Repo:        "acme/widgets",
		Instruction: "Address PR feedback",
		Trigger:     "pr_comment",
		IssueNumber: 42,
		PRNumber:    137,
		Context:     "Please handle the review comments.",
		Debug:       true,
		RunDir:      runDir,
	}

	if err := s.WriteRunFiles(run); err != nil {
		t.Fatalf("WriteRunFiles() error = %v", err)
	}

	data, err := os.ReadFile(filepath.Join(runDir, "context.json"))
	if err != nil {
		t.Fatalf("ReadFile(context.json) error = %v", err)
	}

	var got RunContextFile
	if err := json.Unmarshal(data, &got); err != nil {
		t.Fatalf("Unmarshal(context.json) error = %v", err)
	}

	want := RunContextFile{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		Trigger:     run.Trigger.String(),
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	}
	if got != want {
		t.Fatalf("context.json mismatch: got %#v want %#v", got, want)
	}

	persistentData, err := os.ReadFile(filepath.Join(runDir, "persistent_instructions.md"))
	if err != nil {
		t.Fatalf("ReadFile(persistent_instructions.md) error = %v", err)
	}
	persistentText := string(persistentData)
	for _, want := range []string{
		"# Rascal Persistent Instructions",
		"Do not ask for interactive input.",
		"/rascal-meta/commit_message.txt",
	} {
		if !strings.Contains(persistentText, want) {
			t.Fatalf("persistent instructions missing %q\nfull text:\n%s", want, persistentText)
		}
	}
}

func TestBuildHeadBranchUsesTaskSummaryForAdHocRunTaskID(t *testing.T) {
	t.Parallel()
	got := BuildHeadBranch(
		"run_97073bc1e7787f7c",
		"When running bootstrap with --skip-deploy, preserve host/domain values.\n\nKeep it small.",
		"run_97073bc1e7787f7c",
	)
	if !strings.HasPrefix(got, "rascal/when-running-bootstrap") {
		t.Fatalf("expected summary-based branch prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "-97073bc1e7") {
		t.Fatalf("expected short run-id suffix, got %q", got)
	}
}

func TestBuildHeadBranchUsesTaskIDForNamedTasks(t *testing.T) {
	t.Parallel()
	got := BuildHeadBranch("owner/repo#123", "ignored task text", "run_deadbeefcafefeed")
	if !strings.HasPrefix(got, "rascal/owner/repo-123-") {
		t.Fatalf("expected task-id-based branch prefix, got %q", got)
	}
	if !strings.HasSuffix(got, "-deadbeefca") {
		t.Fatalf("expected short run-id suffix, got %q", got)
	}
}
