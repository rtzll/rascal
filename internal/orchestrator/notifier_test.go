package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

type testIssueComment struct {
	repo        string
	issueNumber int
	body        string
}

type testIssueReaction struct {
	repo        string
	issueNumber int
	content     string
}

type testIssueCommentReaction struct {
	repo      string
	commentID int64
	content   string
}

type testReviewReaction struct {
	repo       string
	pullNumber int
	reviewID   int64
	content    string
}

type testReviewCommentReaction struct {
	repo      string
	commentID int64
	content   string
}

type recordingGitHubClient struct {
	issueComments          []testIssueComment
	issueReactions         []testIssueReaction
	removedIssueReactions  []testIssueReaction
	issueCommentReactions  []testIssueCommentReaction
	reviewReactions        []testReviewReaction
	reviewCommentReactions []testReviewCommentReaction
}

func (r *recordingGitHubClient) GetIssue(context.Context, string, int) (ghapi.IssueData, error) {
	return ghapi.IssueData{}, nil
}

func (r *recordingGitHubClient) GetPullRequest(context.Context, string, int) (ghapi.PullRequest, error) {
	return ghapi.PullRequest{}, nil
}

func (r *recordingGitHubClient) AddIssueReaction(_ context.Context, repo string, issueNumber int, content string) error {
	r.issueReactions = append(r.issueReactions, testIssueReaction{repo: repo, issueNumber: issueNumber, content: content})
	return nil
}

func (r *recordingGitHubClient) RemoveIssueReactions(_ context.Context, repo string, issueNumber int) error {
	r.removedIssueReactions = append(r.removedIssueReactions, testIssueReaction{repo: repo, issueNumber: issueNumber})
	return nil
}

func (r *recordingGitHubClient) AddIssueCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	r.issueCommentReactions = append(r.issueCommentReactions, testIssueCommentReaction{repo: repo, commentID: commentID, content: content})
	return nil
}

func (r *recordingGitHubClient) AddPullRequestReviewReaction(_ context.Context, repo string, pullNumber int, reviewID int64, content string) error {
	r.reviewReactions = append(r.reviewReactions, testReviewReaction{repo: repo, pullNumber: pullNumber, reviewID: reviewID, content: content})
	return nil
}

func (r *recordingGitHubClient) AddPullRequestReviewCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	r.reviewCommentReactions = append(r.reviewCommentReactions, testReviewCommentReaction{repo: repo, commentID: commentID, content: content})
	return nil
}

func (r *recordingGitHubClient) CreateIssueComment(_ context.Context, repo string, issueNumber int, body string) error {
	r.issueComments = append(r.issueComments, testIssueComment{repo: repo, issueNumber: issueNumber, body: body})
	return nil
}

func (r *recordingGitHubClient) ListIssueComments(_ context.Context, repo string, issueNumber int) ([]ghapi.Comment, error) {
	comments := make([]ghapi.Comment, 0, len(r.issueComments))
	for _, comment := range r.issueComments {
		if comment.repo != repo || comment.issueNumber != issueNumber {
			continue
		}
		comments = append(comments, ghapi.Comment{Body: comment.body})
	}
	return comments, nil
}

func TestGitHubRunNotifierNotifyRunStartedIdempotent(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	startedAt := time.Now().UTC()
	run := state.Run{
		ID:           "run_start",
		Repo:         "owner/repo",
		RunDir:       runDir,
		IssueNumber:  42,
		Trigger:      runtrigger.NameIssueLabel,
		AgentRuntime: runtime.RuntimeCodex,
		CreatedAt:    startedAt.Add(-2 * time.Second),
		StartedAt:    &startedAt,
	}

	notifier.NotifyRunStarted(run, runtime.SessionModeAll, true)
	notifier.NotifyRunStarted(run, runtime.SessionModeAll, true)

	if got := len(gh.issueComments); got != 1 {
		t.Fatalf("issue comments = %d, want 1", got)
	}
	if got := gh.issueComments[0]; got.repo != "owner/repo" || got.issueNumber != 42 {
		t.Fatalf("comment target = %#v, want owner/repo#42", got)
	}
	if body := gh.issueComments[0].body; !containsAll(body, runStartCommentBodyMarker, "run_start") {
		t.Fatalf("start comment body missing marker/run id: %q", body)
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindStart); err != nil {
		t.Fatalf("GetRunNotification(start): %v", err)
	} else if !ok {
		t.Fatal("expected start notification record")
	}
}

func TestGitHubRunNotifierNotifyRunCompletedIdempotent(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 77,
		RequestedBy: "rtzll",
		Trigger:     runtrigger.NamePRComment,
	})
	if err := os.WriteFile(filepath.Join(runDir, agentLogFile), []byte("implemented the requested change"), 0o644); err != nil {
		t.Fatalf("write agent log: %v", err)
	}

	run, err := store.AddRun(state.CreateRunInput{
		ID:           "run_complete",
		TaskID:       "owner/repo#55",
		Repo:         "owner/repo",
		Instruction:  "Address PR #77 feedback",
		AgentRuntime: runtime.RuntimeGooseCodex,
		Trigger:      runtrigger.NamePRComment,
		RunDir:       runDir,
		IssueNumber:  55,
		PRNumber:     77,
	})
	if err != nil {
		t.Fatalf("store.AddRun: %v", err)
	}

	notifier.NotifyRunCompleted(run)
	notifier.NotifyRunCompleted(run)

	if got := len(gh.issueComments); got != 1 {
		t.Fatalf("issue comments = %d, want 1", got)
	}
	if got := gh.issueComments[0]; got.repo != "owner/repo" || got.issueNumber != 77 {
		t.Fatalf("comment target = %#v, want owner/repo#77", got)
	}
	if body := gh.issueComments[0].body; !containsAll(body, runCompletionCommentBodyMarker, runCompletionCommentToken(run.ID), "run_complete", "implemented the requested change") {
		t.Fatalf("completion comment body missing expected content: %q", body)
	}
	persisted, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("store.GetRun(%q) not found", run.ID)
	}
	if persisted.CompletionCommentState != state.CompletionCommentStatePosted {
		t.Fatalf("completion comment state = %q, want %q", persisted.CompletionCommentState, state.CompletionCommentStatePosted)
	}
	if persisted.CompletionCommentPostedAt == nil {
		t.Fatalf("completion comment posted_at not recorded")
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindCompletion); err != nil {
		t.Fatalf("GetRunNotification(completion): %v", err)
	} else if !ok {
		t.Fatal("expected completion notification record")
	}
}

func TestGitHubRunNotifierNotifyRunFailedIdempotent(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	run := state.Run{
		ID:           "run_failed",
		Repo:         "owner/repo",
		RunDir:       runDir,
		IssueNumber:  91,
		Trigger:      runtrigger.NameIssueLabel,
		AgentRuntime: runtime.RuntimeCodex,
		Error:        "stage pr_create: gh pr create failed",
		CreatedAt:    time.Now().UTC(),
	}

	notifier.NotifyRunFailed(run)
	notifier.NotifyRunFailed(run)

	if got := len(gh.issueComments); got != 1 {
		t.Fatalf("issue comments = %d, want 1", got)
	}
	if body := gh.issueComments[0].body; !containsAll(body, runFailureCommentBodyMarker, "run_failed", "gh pr create failed") {
		t.Fatalf("failure comment body missing expected content: %q", body)
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindFailure); err != nil {
		t.Fatalf("GetRunNotification(failure): %v", err)
	} else if !ok {
		t.Fatal("expected failure notification record")
	}
}

func TestGitHubRunNotifierReactionsAndRuntimeErrors(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	notifier.ReactToIssue("owner/repo", 11, ghapi.ReactionEyes)
	notifier.ClearIssueReactions("owner/repo", 11)
	notifier.ReactToIssueComment("owner/repo", 22, ghapi.ReactionRocket)
	notifier.ReactToPullRequestReview("owner/repo", 33, 44, ghapi.ReactionHooray)
	notifier.ReactToPullRequestReviewComment("owner/repo", 55, ghapi.ReactionConfused)
	notifier.NotifyInvalidRuntimeLabel("owner/repo", 66, errors.New("unknown runtime in label \"rascal:gpt4\""))

	if got := len(gh.issueReactions); got != 1 {
		t.Fatalf("issue reactions = %d, want 1", got)
	}
	if got := len(gh.removedIssueReactions); got != 1 {
		t.Fatalf("removed issue reactions = %d, want 1", got)
	}
	if got := len(gh.issueCommentReactions); got != 1 {
		t.Fatalf("issue comment reactions = %d, want 1", got)
	}
	if got := len(gh.reviewReactions); got != 1 {
		t.Fatalf("review reactions = %d, want 1", got)
	}
	if got := len(gh.reviewCommentReactions); got != 1 {
		t.Fatalf("review comment reactions = %d, want 1", got)
	}
	if got := len(gh.issueComments); got != 1 {
		t.Fatalf("issue comments = %d, want 1", got)
	}
	if body := gh.issueComments[0].body; !containsAll(body, "Unknown agent runtime in label.", "rascal:claude", "rascal:codex") {
		t.Fatalf("runtime error comment missing expected content: %q", body)
	}
}

func newNotifierTestStore(t *testing.T) *state.Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.db")
	store, err := state.New(path, 200)
	if err != nil {
		t.Fatalf("state.New: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("store.Close: %v", err)
		}
	})
	return store
}

func writeNotifierJSON(t *testing.T, path string, value any) {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("json.Marshal(%s): %v", path, err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatalf("os.WriteFile(%s): %v", path, err)
	}
}

func containsAll(haystack string, needles ...string) bool {
	for _, needle := range needles {
		if !strings.Contains(haystack, needle) {
			return false
		}
	}
	return true
}
