package orchestrator

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

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

func TestCreateAndQueueRunWritesResponseTarget(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#99",
		Repo:        "owner/repo",
		Instruction: "Address PR #99 feedback",
		Trigger:     "pr_comment",
		PRNumber:    99,
		ResponseTarget: &RunResponseTarget{
			RequestedBy: " alice ",
		},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	target, ok, err := s.Store.GetRunResponseTarget(run.ID)
	if err != nil {
		t.Fatalf("get run response target: %v", err)
	}
	if !ok {
		t.Fatal("expected persisted run response target")
	}
	if target.Repo != "owner/repo" {
		t.Fatalf("target repo = %q, want owner/repo", target.Repo)
	}
	if target.IssueNumber != 99 {
		t.Fatalf("target issue number = %d, want 99", target.IssueNumber)
	}
	if target.RequestedBy != "alice" {
		t.Fatalf("target requested_by = %q, want alice", target.RequestedBy)
	}
	if target.Trigger != "pr_comment" {
		t.Fatalf("target trigger = %q, want pr_comment", target.Trigger)
	}
	if target.ReviewThreadID != 0 {
		t.Fatalf("target review_thread_id = %d, want 0", target.ReviewThreadID)
	}
	if _, ok, err := LoadRunResponseTarget(run.RunDir); err != nil {
		t.Fatalf("load legacy run response target: %v", err)
	} else if ok {
		t.Fatal("expected no legacy response_target.json for new runs")
	}
}

func TestCreateAndQueueRunWritesReviewThreadResponseTarget(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#100",
		Repo:        "owner/repo",
		Instruction: "Address PR #100 unresolved review thread",
		Trigger:     "pr_review_thread",
		PRNumber:    100,
		ResponseTarget: &RunResponseTarget{
			RequestedBy:    " bob ",
			ReviewThreadID: 42,
		},
	})
	if err != nil {
		t.Fatalf("create run: %v", err)
	}

	target, ok, err := s.Store.GetRunResponseTarget(run.ID)
	if err != nil {
		t.Fatalf("get run response target: %v", err)
	}
	if !ok {
		t.Fatal("expected persisted run response target")
	}
	if target.ReviewThreadID != 42 {
		t.Fatalf("target review_thread_id = %d, want 42", target.ReviewThreadID)
	}
	if target.RequestedBy != "bob" {
		t.Fatalf("target requested_by = %q, want bob", target.RequestedBy)
	}
}

func TestCreateAndQueueRunSerializesPerTask(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	var closeWait sync.Once
	stop := func() { closeWait.Do(func() { close(waitCh) }) }
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	defer func() {
		stop()
		waitForExecutionServerIdle(t, s)
	}()

	_, err := s.CreateAndQueueRun(RunRequest{TaskID: "task-1", Repo: "owner/repo", Instruction: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.CreateAndQueueRun(RunRequest{TaskID: "task-1", Repo: "owner/repo", Instruction: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}

	waitForExecutionCondition(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first run to start only")
	launcher.mu.Lock()
	firstSpecCount := len(launcher.specs)
	firstSpecDebug := false
	if firstSpecCount > 0 {
		firstSpecDebug = launcher.specs[0].Debug
	}
	launcher.mu.Unlock()
	if firstSpecCount == 0 || !firstSpecDebug {
		t.Fatalf("expected first run spec debug=true, got count=%d debug=%t", firstSpecCount, firstSpecDebug)
	}
	r2, ok := s.Store.GetRun(second.ID)
	if !ok {
		t.Fatalf("missing second run %s", second.ID)
	}
	if r2.Status != state.StatusQueued {
		t.Fatalf("expected second run queued, got %s", r2.Status)
	}

	stop()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		return launcher.Calls() == 2
	}, "second run to start after first completes")
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		r, ok := s.Store.GetRun(second.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "second run to complete")
}

func TestCreateAndQueueRunRespectsGlobalConcurrencyLimit(t *testing.T) {
	t.Parallel()
	waitCh := make(chan struct{})
	var closeWait sync.Once
	stop := func() { closeWait.Do(func() { close(waitCh) }) }
	launcher := &executionFakeRunner{waitCh: waitCh}
	s := newExecutionTestServer(t, launcher)
	s.MaxConcurrent = 1
	defer func() {
		stop()
		waitForExecutionServerIdle(t, s)
	}()

	_, err := s.CreateAndQueueRun(RunRequest{TaskID: "task-1", Repo: "owner/repo", Instruction: "first"})
	if err != nil {
		t.Fatalf("create first run: %v", err)
	}
	second, err := s.CreateAndQueueRun(RunRequest{TaskID: "task-2", Repo: "owner/repo", Instruction: "second"})
	if err != nil {
		t.Fatalf("create second run: %v", err)
	}

	waitForExecutionCondition(t, time.Second, func() bool { return launcher.Calls() == 1 }, "only one run starts while at concurrency limit")
	r2, ok := s.Store.GetRun(second.ID)
	if !ok {
		t.Fatalf("missing second run %s", second.ID)
	}
	if r2.Status != state.StatusQueued {
		t.Fatalf("expected second run queued, got %s", r2.Status)
	}

	stop()
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		return launcher.Calls() == 2
	}, "second run to start after slot is available")
	waitForExecutionCondition(t, 2*time.Second, func() bool {
		r, ok := s.Store.GetRun(second.ID)
		return ok && r.Status == state.StatusSucceeded
	}, "second run to complete")
}
