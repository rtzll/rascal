package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

func TestExecuteRunDoesNotRequeueSuccessfulRunWhenTranscriptMentionsUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			PRNumber: 124,
			PRURL:    "https://github.com/owner/repo/pull/124",
			HeadSHA:  "abc123",
			ExitCode: 0,
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_success_usage_limit_false_positive",
		TaskID:      "owner/repo#54",
		Repo:        "owner/repo",
		Instruction: "Implement review thread webhook handling",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-54",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 54,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent_output.txt"), []byte("Implemented the requested change and opened a pull request."), 0o644); err != nil {
		t.Fatalf("write agent output: %v", err)
	}
	transcript := `{"type":"item.completed","item":{"type":"command_execution","aggregated_output":"cmd/rascald/main_test.go:2294: gooseLog := Request failed: Codex CLI error: You've hit your usage limit. Try again at Mar 10th, 2099 6:31 AM."}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(transcript+"\n"), 0o644); err != nil {
		t.Fatalf("write agent transcript: %v", err)
	}

	s.ExecuteRun(run.ID)

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusReview {
		t.Fatalf("expected review status after successful run, got %s", updated.Status)
	}
	if pauseUntil, reason, ok, err := s.Store.ActiveSchedulerPause(SchedulerPauseScope, time.Now().UTC()); err != nil {
		t.Fatalf("load scheduler pause: %v", err)
	} else if ok {
		t.Fatalf("did not expect scheduler pause, got until=%s reason=%q", pauseUntil, reason)
	}
}

func TestScheduleRunsResumesAfterPauseDeadline(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	pauseUntil := time.Now().UTC().Add(150 * time.Millisecond)
	if _, err := s.Store.PauseScheduler(SchedulerPauseScope, "test pause", pauseUntil); err != nil {
		t.Fatalf("pause scheduler: %v", err)
	}

	if _, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "owner/repo#resume",
		Repo:        "owner/repo",
		Instruction: "resume after pause",
	}); err != nil {
		t.Fatalf("create run: %v", err)
	}

	if calls := launcher.Calls(); calls != 0 {
		t.Fatalf("expected scheduler to stay paused before deadline, got calls=%d", calls)
	}

	waitForExecutionCondition(t, 2*time.Second, func() bool { return launcher.Calls() == 1 }, "scheduler resume after pause deadline")
}
