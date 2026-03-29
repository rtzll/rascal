package orchestrator

import (
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
