package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/state"
)

func findExecutionComment(comments []testIssueComment, marker string) (testIssueComment, bool) {
	for _, comment := range comments {
		if strings.Contains(comment.body, marker) {
			return comment, true
		}
	}
	return testIssueComment{}, false
}

func TestExecuteRunPostsCompletionCommentForCommentTriggeredRun(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			PRNumber: 77,
			PRURL:    "https://example.com/pr/77",
			HeadSHA:  "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	gh := &recordingGitHubClient{}
	s.GitHub = gh
	s.Config.GitHubToken = "token"
	s.Notifier = NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, s.Store, gh)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_comment_completion",
		TaskID:      "owner/repo#77",
		Repo:        "owner/repo",
		Instruction: "Address PR #77 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-77",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    77,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	writeNotifierJSON(t, filepath.Join(run.RunDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 77,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	})
	if err := os.WriteFile(filepath.Join(run.RunDir, "commit_message.txt"), []byte("fix(rascal): address feedback\n\n- updated handlers\n- added tests\n"), 0o644); err != nil {
		t.Fatalf("write commit message: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(`{"event":"x","usage":{"total_tokens":123000}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write agent log: %v", err)
	}

	s.ExecuteRun(run.ID)

	comments := gh.comments()
	if len(comments) != 2 {
		t.Fatalf("expected start and completion comments, got %d", len(comments))
	}
	startComment, ok := findExecutionComment(comments, RunStartCommentBodyMarker)
	if !ok {
		t.Fatalf("expected start comment, got %+v", comments)
	}
	if startComment.repo != "owner/repo" || startComment.issueNumber != 77 {
		t.Fatalf("unexpected start comment target: %+v", startComment)
	}
	if !strings.Contains(startComment.body, "Rascal started run `run_comment_completion` to address new PR feedback.") {
		t.Fatalf("expected concise start summary, got:\n%s", startComment.body)
	}
	if !strings.Contains(startComment.body, "<details><summary>Run Settings</summary>") {
		t.Fatalf("expected settings details in start comment, got:\n%s", startComment.body)
	}
	completionComment, ok := findExecutionComment(comments, RunCompletionCommentBodyMarker)
	if !ok {
		t.Fatalf("expected completion comment, got %+v", comments)
	}
	if !strings.Contains(completionComment.body, "@alice implemented in commit [`0123456789ab`]") {
		t.Fatalf("expected requester mention with short sha, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Closes #16") {
		t.Fatalf("expected original issue reference, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "- updated handlers") {
		t.Fatalf("expected commit body bullets in comment, got:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "<details><summary>Agent Details</summary>") {
		t.Fatalf("expected agent details section, got:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Rascal run `run_comment_completion` completed in ") || !strings.Contains(completionComment.body, "123K tokens") {
		t.Fatalf("expected runtime and token summary, got:\n%s", completionComment.body)
	}
	usage, ok := s.Store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected persisted token usage for %s", run.ID)
	}
	if usage.TotalTokens != 123000 {
		t.Fatalf("total_tokens = %d, want 123000", usage.TotalTokens)
	}
}

func TestExecuteRunPostsDetailsWithoutCommitClaimWhenCommitMessageMissing(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			PRNumber: 52,
			PRURL:    "https://example.com/pr/52",
			HeadSHA:  "0109106ceba61adf1735bc980f83c15506b8da7a",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	gh := &recordingGitHubClient{}
	s.GitHub = gh
	s.Config.GitHubToken = "token"
	s.Notifier = NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, s.Store, gh)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_comment_no_commit",
		TaskID:      "owner/repo#16",
		Repo:        "owner/repo",
		Instruction: "Address PR #52 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-52",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    52,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	writeNotifierJSON(t, filepath.Join(run.RunDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 52,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	})
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(`{"type":"message","message":{"content":[{"type":"text","text":"Request failed"}]}}`+"\n"), 0o644); err != nil {
		t.Fatalf("write agent log: %v", err)
	}

	s.ExecuteRun(run.ID)

	comments := gh.comments()
	if len(comments) != 2 {
		t.Fatalf("expected start and completion comments, got %d", len(comments))
	}
	startComment, ok := findExecutionComment(comments, RunStartCommentBodyMarker)
	if !ok {
		t.Fatalf("expected start comment, got %+v", comments)
	}
	if startComment.issueNumber != 52 {
		t.Fatalf("start comment target issue number = %d, want 52", startComment.issueNumber)
	}
	completionComment, ok := findExecutionComment(comments, RunCompletionCommentBodyMarker)
	if !ok {
		t.Fatalf("expected completion comment, got %+v", comments)
	}
	if strings.Contains(completionComment.body, "implemented in commit") {
		t.Fatalf("did not expect commit claim without commit message, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "@alice posted the run details below.") {
		t.Fatalf("expected neutral requester summary, got body:\n%s", completionComment.body)
	}
	if !strings.Contains(completionComment.body, "Closes #16") {
		t.Fatalf("expected original issue reference, got body:\n%s", completionComment.body)
	}
}

func TestExecuteRunRequeuesRunForGooseUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			ExitCode: 1,
			Error:    "goose run failed: exit status 1",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	gh := &recordingGitHubClient{}
	s.GitHub = gh
	s.Config.GitHubToken = "token"
	s.Notifier = NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, s.Store, gh)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_comment_usage_limit",
		TaskID:      "owner/repo#53",
		Repo:        "owner/repo",
		Instruction: "Address PR #53 feedback",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-53",
		Trigger:     "pr_comment",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
		PRNumber:    53,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	writeNotifierJSON(t, filepath.Join(run.RunDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 53,
		RequestedBy: "alice",
		Trigger:     "pr_comment",
	})
	gooseLog := `{"type":"message","message":{"id":null,"role":"assistant","created":1772899608,"content":[{"type":"text","text":"Ran into this error: Request failed: Codex CLI error: You've hit your usage limit. To get more access now, send a request to your admin or try again at Mar 10th, 2099 6:31 AM..\n\nPlease retry if you think this is a transient or recoverable error."}],"metadata":{"userVisible":true,"agentVisible":true}}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(gooseLog+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.ExecuteRun(run.ID)

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after usage limit, got %s", updated.Status)
	}
	if updated.StartedAt != nil {
		t.Fatalf("expected started_at cleared on requeue")
	}
	if updated.CompletedAt != nil {
		t.Fatalf("expected completed_at cleared on requeue")
	}
	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected only the start comment while run is paused for retry, got %d", len(comments))
	}
	if _, ok := findExecutionComment(comments, RunStartCommentBodyMarker); !ok {
		t.Fatalf("expected start comment while paused for retry, got %+v", comments)
	}
	if calls := launcher.Calls(); calls != 1 {
		t.Fatalf("expected run not to restart before pause expiry, got launcher calls=%d", calls)
	}
	if _, ok, err := s.Store.GetRunNotification(run.ID, state.RunNotificationKindFailure); err != nil {
		t.Fatalf("GetRunNotification(failure): %v", err)
	} else if ok {
		t.Fatal("expected no failure notification record")
	}
	if pauseUntil, reason, ok, err := s.Store.ActiveSchedulerPause(SchedulerPauseScope, time.Now().UTC()); err != nil {
		t.Fatalf("load scheduler pause: %v", err)
	} else if !ok {
		t.Fatal("expected active scheduler pause after usage limit")
	} else {
		if !pauseUntil.After(time.Now().UTC()) {
			t.Fatalf("expected future pause deadline, got %s", pauseUntil)
		}
		if !strings.Contains(reason, "usage limit") {
			t.Fatalf("expected usage-limit pause reason, got %q", reason)
		}
	}
}

func TestExecuteRunRequeuesIssueTriggeredRunForUsageLimit(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			ExitCode: 1,
			Error:    "goose run failed: exit status 1",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	gh := &recordingGitHubClient{}
	s.GitHub = gh
	s.Config.GitHubToken = "token"
	s.Notifier = NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, s.Store, gh)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_issue_usage_limit",
		TaskID:      "owner/repo#16",
		Repo:        "owner/repo",
		Instruction: "Investigate issue #16",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-16",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
		IssueNumber: 16,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	writeNotifierJSON(t, filepath.Join(run.RunDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 16,
		RequestedBy: "alice",
		Trigger:     "issue_label",
	})
	gooseLog := `{"type":"message","message":{"content":[{"type":"text","text":"Request failed: Codex CLI error: You've hit your usage limit. Try again at Mar 10th, 2099 6:31 AM."}]}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte(gooseLog+"\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	s.ExecuteRun(run.ID)

	updated, ok := s.Store.GetRun(run.ID)
	if !ok {
		t.Fatalf("missing run %s", run.ID)
	}
	if updated.Status != state.StatusQueued {
		t.Fatalf("expected queued status after usage limit, got %s", updated.Status)
	}
	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected only the start comment while run is paused for retry, got %d", len(comments))
	}
	if _, ok := findExecutionComment(comments, RunStartCommentBodyMarker); !ok {
		t.Fatalf("expected start comment while paused for retry, got %+v", comments)
	}
	if calls := launcher.Calls(); calls != 1 {
		t.Fatalf("expected run not to restart before pause expiry, got launcher calls=%d", calls)
	}
}
