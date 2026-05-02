package orchestrator

import (
	"context"
	"encoding/json"
	"testing"

	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func TestWebhookInterpreterIssueLabelCreatesRun(t *testing.T) {
	actions := interpretWebhookActions(t, "issues", ghapi.IssuesEvent{
		Action: "labeled",
		Label:  ghapi.Label{Name: "rascal"},
		Issue: ghapi.Issue{
			Number: 12,
			Title:  "Refactor this",
			Body:   "Please handle it",
			Labels: []ghapi.Label{{Name: "rascal"}},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "rtzll"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCreateIssueRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	action := actions[0]
	if action.TaskID != "owner/repo#12" {
		t.Fatalf("task id = %q, want owner/repo#12", action.TaskID)
	}
	if action.Trigger != runtrigger.NameIssueLabel {
		t.Fatalf("trigger = %q, want %q", action.Trigger, runtrigger.NameIssueLabel)
	}
	if action.Context == "" || action.Instruction == "" {
		t.Fatalf("expected populated context and instruction: %#v", action)
	}
}

func TestWebhookInterpreterIssueEditedCancelsAndCreatesRun(t *testing.T) {
	actions := interpretWebhookActions(t, "issues", ghapi.IssuesEvent{
		Action: "edited",
		Issue: ghapi.Issue{
			Number: 7,
			Title:  "Refactor notifier",
			Body:   "Update webhook handling",
			Labels: []ghapi.Label{{Name: "rascal"}},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "rtzll"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCancelTaskRuns, WebhookActionCreateIssueRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].StatusReason != state.RunStatusReasonIssueEdited {
		t.Fatalf("status reason = %q, want %q", actions[0].StatusReason, state.RunStatusReasonIssueEdited)
	}
	if actions[1].Trigger != runtrigger.NameIssueEdited {
		t.Fatalf("trigger = %q, want %q", actions[1].Trigger, runtrigger.NameIssueEdited)
	}
}

func TestWebhookInterpreterIssueClosedCompletesTask(t *testing.T) {
	actions := interpretWebhookActions(t, "issues", ghapi.IssuesEvent{
		Action: "closed",
		Issue: ghapi.Issue{
			Number: 5,
			Labels: []ghapi.Label{{Name: "rascal"}},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCompleteIssueTask}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
}

func TestWebhookInterpreterIssueCommentCreatedQueuesPRRun(t *testing.T) {
	actions := interpretWebhookActions(t, "issue_comment", ghapi.IssueCommentEvent{
		Action: "created",
		Issue: ghapi.Issue{
			Number:      22,
			State:       "open",
			PullRequest: &ghapi.PullRequestRef{URL: "https://example.invalid/pr/22"},
		},
		Comment: ghapi.Comment{
			ID:   101,
			Body: "Please address the feedback",
			User: ghapi.User{Login: "reviewer"},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "reviewer"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCreatePRCommentRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].CommentID != 101 {
		t.Fatalf("comment id = %d, want 101", actions[0].CommentID)
	}
}

func TestWebhookInterpreterIssueCommentIgnoresAutomationComment(t *testing.T) {
	actions := interpretWebhookActions(t, "issue_comment", ghapi.IssueCommentEvent{
		Action: "created",
		Issue: ghapi.Issue{
			Number:      22,
			State:       "open",
			PullRequest: &ghapi.PullRequestRef{URL: "https://example.invalid/pr/22"},
		},
		Comment: ghapi.Comment{
			ID:   101,
			Body: "<!-- rascal:completion-comment -->",
			User: ghapi.User{Login: "reviewer"},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "reviewer"},
	})

	if len(actions) != 0 {
		t.Fatalf("actions = %v, want none", actionKinds(actions))
	}
}

func TestWebhookInterpreterPullRequestReviewSubmittedQueuesRun(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request_review", ghapi.PullRequestReviewEvent{
		Action: "submitted",
		Review: ghapi.Review{
			ID:    44,
			State: "commented",
			User:  ghapi.User{Login: "reviewer"},
		},
		PullRequest: ghapi.PullRequest{
			Number: 33,
			State:  "open",
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "reviewer"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCreatePRReviewRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].Context != "review state: commented" {
		t.Fatalf("context = %q, want review-state fallback", actions[0].Context)
	}
}

func TestWebhookInterpreterPullRequestReviewCommentIncludesLocation(t *testing.T) {
	line := 17
	actions := interpretWebhookActions(t, "pull_request_review_comment", ghapi.PullRequestReviewCommentEvent{
		Action: "created",
		Comment: ghapi.ReviewComment{
			ID:   55,
			Body: "Please rename this",
			Path: "internal/orchestrator/webhook.go",
			Line: &line,
			User: ghapi.User{Login: "reviewer"},
		},
		PullRequest: ghapi.PullRequest{
			Number: 33,
			State:  "open",
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "reviewer"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCreatePRReviewCommentRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].Context == "" || actions[0].CommentID != 55 {
		t.Fatalf("expected populated context and comment id: %#v", actions[0])
	}
}

func TestWebhookInterpreterPullRequestReviewThreadResolvedCancelsRuns(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request_review_thread", ghapi.PullRequestReviewThreadEvent{
		Action: "resolved",
		Thread: ghapi.ReviewThread{ID: 909},
		PullRequest: ghapi.PullRequest{
			Number: 77,
			State:  "open",
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCancelPRThreadRuns}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].ReviewThreadID != 909 {
		t.Fatalf("review thread id = %d, want 909", actions[0].ReviewThreadID)
	}
}

func TestWebhookInterpreterPullRequestClosedMergedReconciles(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request", ghapi.PullRequestEvent{
		Action: "closed",
		PullRequest: ghapi.PullRequest{
			Number: 88,
			Merged: true,
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionClosePullRequest}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if !actions[0].Merged {
		t.Fatalf("merged = false, want true")
	}
}

func TestWebhookInterpreterPullRequestConvertedToDraftPauses(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request", ghapi.PullRequestEvent{
		Action: "converted_to_draft",
		PullRequest: ghapi.PullRequest{
			Number: 88,
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionConvertPullRequestDraft}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
}

func TestWebhookInterpreterPullRequestReadyForReviewResumes(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request", ghapi.PullRequestEvent{
		Action: "ready_for_review",
		PullRequest: ghapi.PullRequest{
			Number: 88,
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionReadyPullRequest}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
}

func TestWebhookInterpreterPullRequestSynchronizeCancelsAndRequeues(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request", ghapi.PullRequestEvent{
		Action: "synchronize",
		PullRequest: ghapi.PullRequest{
			Number: 88,
			State:  "open",
			Base: struct {
				Ref string `json:"ref"`
			}{Ref: "main"},
			Head: struct {
				Ref string `json:"ref"`
				SHA string `json:"sha"`
			}{Ref: "feature/new-head", SHA: "abc123"},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "reviewer"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionSynchronizePullRequest}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].Trigger != runtrigger.NamePRSynchronize {
		t.Fatalf("trigger = %q, want %q", actions[0].Trigger, runtrigger.NamePRSynchronize)
	}
	if actions[0].StatusReason != state.RunStatusReasonPRSynchronized {
		t.Fatalf("status reason = %q, want %q", actions[0].StatusReason, state.RunStatusReasonPRSynchronized)
	}
	if actions[0].HeadBranch != "feature/new-head" || actions[0].HeadSHA != "abc123" {
		t.Fatalf("expected head branch/sha to be preserved: %#v", actions[0])
	}
}

func TestWebhookInterpreterPullRequestSynchronizeIgnoresBotActor(t *testing.T) {
	actions := interpretWebhookActions(t, "pull_request", ghapi.PullRequestEvent{
		Action: "synchronize",
		PullRequest: ghapi.PullRequest{
			Number: 88,
			State:  "open",
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
		Sender:     ghapi.User{Login: "rascal[bot]"},
	})

	if len(actions) != 0 {
		t.Fatalf("actions = %v, want none", actionKinds(actions))
	}
}

func TestWebhookSynchronizeDoesNotQueueRunWhenPRIdle(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
		ID:          "task-pr-idle",
		Repo:        "owner/repo",
		IssueNumber: 42,
		PRNumber:    88,
	}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if _, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_completed",
		TaskID:      "task-pr-idle",
		Repo:        "owner/repo",
		Instruction: "done",
		PRNumber:    88,
		RunDir:      t.TempDir(),
	}); err != nil {
		t.Fatalf("add completed run: %v", err)
	}
	if _, err := s.Store.SetRunStatus("run_completed", state.StatusRunning, ""); err != nil {
		t.Fatalf("mark run running: %v", err)
	}
	if _, err := s.Store.SetRunStatus("run_completed", state.StatusSucceeded, ""); err != nil {
		t.Fatalf("mark run succeeded: %v", err)
	}

	if err := s.executeWebhookAction(context.Background(), WebhookAction{
		Kind:         WebhookActionSynchronizePullRequest,
		Repo:         "owner/repo",
		PRNumber:     88,
		Trigger:      runtrigger.NamePRSynchronize,
		RequestedBy:  "rtzll",
		BaseBranch:   "main",
		HeadBranch:   "feature",
		HeadSHA:      "new-sha",
		CancelReason: "pull request synchronized",
		StatusReason: state.RunStatusReasonPRSynchronized,
	}); err != nil {
		t.Fatalf("execute synchronize action: %v", err)
	}

	runs := s.Store.ListRuns(10)
	if len(runs) != 1 {
		t.Fatalf("run count = %d, want 1: %#v", len(runs), runs)
	}
	if runs[0].ID != "run_completed" || runs[0].Status != state.StatusSucceeded {
		t.Fatalf("unexpected run after idle synchronize: %#v", runs[0])
	}
}

func TestWebhookSynchronizeReplacesActiveRun(t *testing.T) {
	t.Parallel()
	s := newExecutionTestServer(t, &executionFakeRunner{})
	defer waitForExecutionServerIdle(t, s)

	if _, err := s.CreateAndQueueRun(RunRequest{
		TaskID:      "task-pr-active",
		Repo:        "owner/repo",
		Instruction: "Update dependency",
		Trigger:     runtrigger.NamePRComment,
		IssueNumber: 42,
		PRNumber:    88,
		BaseBranch:  "main",
		HeadBranch:  "feature",
	}); err != nil {
		t.Fatalf("create active run: %v", err)
	}

	if err := s.executeWebhookAction(context.Background(), WebhookAction{
		Kind:         WebhookActionSynchronizePullRequest,
		Repo:         "owner/repo",
		PRNumber:     88,
		Trigger:      runtrigger.NamePRSynchronize,
		RequestedBy:  "rtzll",
		BaseBranch:   "main",
		HeadBranch:   "feature",
		HeadSHA:      "new-sha",
		CancelReason: "pull request synchronized",
		StatusReason: state.RunStatusReasonPRSynchronized,
	}); err != nil {
		t.Fatalf("execute synchronize action: %v", err)
	}

	runs := s.Store.ListRuns(10)
	if len(runs) != 2 {
		t.Fatalf("run count = %d, want 2: %#v", len(runs), runs)
	}
	if runs[0].Trigger != runtrigger.NamePRSynchronize {
		t.Fatalf("latest run trigger = %q, want %q", runs[0].Trigger, runtrigger.NamePRSynchronize)
	}
	if runs[0].HeadSHA != "new-sha" {
		t.Fatalf("latest run head sha = %q, want new-sha", runs[0].HeadSHA)
	}
	if runs[0].Status != state.StatusQueued && runs[0].Status != state.StatusRunning {
		t.Fatalf("latest run status = %q, want queued or running", runs[0].Status)
	}
	if runs[1].Status != state.StatusCanceled || runs[1].StatusReason != state.RunStatusReasonPRSynchronized {
		t.Fatalf("stale run not canceled for synchronize: %#v", runs[1])
	}
}

func TestWebhookInterpreterCheckRunCompletedFailureQueuesPRRun(t *testing.T) {
	actions := interpretWebhookActions(t, "check_run", ghapi.CheckRunEvent{
		Action: "completed",
		CheckRun: ghapi.CheckRun{
			Name:       "test",
			Conclusion: "failure",
			HeadSHA:    "abc123",
			CheckSuite: ghapi.CheckSuiteRef{HeadBranch: "rascal/branch"},
			PullRequests: []ghapi.CheckPullRequest{{
				Number: 77,
			}},
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if got, want := actionKinds(actions), []WebhookActionKind{WebhookActionCreatePRCheckFailureRun}; !sameActionKinds(got, want) {
		t.Fatalf("action kinds = %v, want %v", got, want)
	}
	if actions[0].PRNumber != 77 {
		t.Fatalf("pr number = %d, want 77", actions[0].PRNumber)
	}
	if actions[0].Trigger != runtrigger.NamePRCheckFailure {
		t.Fatalf("trigger = %q, want %q", actions[0].Trigger, runtrigger.NamePRCheckFailure)
	}
}

func TestWebhookInterpreterCheckSuiteIgnoresNonRascalBranchWithoutPR(t *testing.T) {
	actions := interpretWebhookActions(t, "check_suite", ghapi.CheckSuiteEvent{
		Action: "completed",
		CheckSuite: ghapi.CheckSuite{
			Conclusion: "failure",
			HeadBranch: "feature/not-rascal",
			HeadSHA:    "abc123",
		},
		Repository: ghapi.Repository{FullName: "owner/repo"},
	})

	if len(actions) != 0 {
		t.Fatalf("actions = %v, want none", actionKinds(actions))
	}
}

func TestWebhookInterpreterUnknownEventIsIgnored(t *testing.T) {
	interpreter := NewWebhookInterpreter("rascal-bot")
	actions, err := interpreter.Interpret("unknown", []byte(`{}`))
	if err != nil {
		t.Fatalf("Interpret returned error: %v", err)
	}
	if len(actions) != 0 {
		t.Fatalf("actions = %v, want none", actionKinds(actions))
	}
}

func interpretWebhookActions(t *testing.T, eventType string, payload any) []WebhookAction {
	t.Helper()
	data, err := json.Marshal(payload)
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}
	actions, err := NewWebhookInterpreter("rascal-bot").Interpret(eventType, data)
	if err != nil {
		t.Fatalf("Interpret returned error: %v", err)
	}
	return actions
}

func actionKinds(actions []WebhookAction) []WebhookActionKind {
	kinds := make([]WebhookActionKind, 0, len(actions))
	for _, action := range actions {
		kinds = append(kinds, action.Kind)
	}
	return kinds
}

func sameActionKinds(got, want []WebhookActionKind) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
