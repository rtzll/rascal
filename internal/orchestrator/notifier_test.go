package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
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
	mu sync.Mutex

	issueComments          []testIssueComment
	issueReactions         []testIssueReaction
	removedIssueReactions  []testIssueReaction
	issueCommentReactions  []testIssueCommentReaction
	reviewReactions        []testReviewReaction
	reviewCommentReactions []testReviewCommentReaction

	createIssueCommentErr             error
	createIssueCommentErrSeq          []error
	createIssueCommentPostsOnErrorSeq []bool
	createIssueCommentCalls           int
}

func (r *recordingGitHubClient) GetIssue(context.Context, string, int) (ghapi.IssueData, error) {
	return ghapi.IssueData{}, nil
}

func (r *recordingGitHubClient) GetPullRequest(context.Context, string, int) (ghapi.PullRequest, error) {
	return ghapi.PullRequest{}, nil
}

func (r *recordingGitHubClient) AddIssueReaction(_ context.Context, repo string, issueNumber int, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issueReactions = append(r.issueReactions, testIssueReaction{repo: repo, issueNumber: issueNumber, content: content})
	return nil
}

func (r *recordingGitHubClient) RemoveIssueReactions(_ context.Context, repo string, issueNumber int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.removedIssueReactions = append(r.removedIssueReactions, testIssueReaction{repo: repo, issueNumber: issueNumber})
	return nil
}

func (r *recordingGitHubClient) AddIssueCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.issueCommentReactions = append(r.issueCommentReactions, testIssueCommentReaction{repo: repo, commentID: commentID, content: content})
	return nil
}

func (r *recordingGitHubClient) AddPullRequestReviewReaction(_ context.Context, repo string, pullNumber int, reviewID int64, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviewReactions = append(r.reviewReactions, testReviewReaction{repo: repo, pullNumber: pullNumber, reviewID: reviewID, content: content})
	return nil
}

func (r *recordingGitHubClient) AddPullRequestReviewCommentReaction(_ context.Context, repo string, commentID int64, content string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.reviewCommentReactions = append(r.reviewCommentReactions, testReviewCommentReaction{repo: repo, commentID: commentID, content: content})
	return nil
}

func (r *recordingGitHubClient) CreateIssueComment(_ context.Context, repo string, issueNumber int, body string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	callIdx := r.createIssueCommentCalls
	r.createIssueCommentCalls++
	err := r.createIssueCommentErr
	postOnError := false
	if callIdx < len(r.createIssueCommentErrSeq) {
		err = r.createIssueCommentErrSeq[callIdx]
	}
	if callIdx < len(r.createIssueCommentPostsOnErrorSeq) {
		postOnError = r.createIssueCommentPostsOnErrorSeq[callIdx]
	}
	if err != nil && !postOnError {
		return err
	}
	r.issueComments = append(r.issueComments, testIssueComment{repo: repo, issueNumber: issueNumber, body: body})
	if err != nil {
		return err
	}
	return nil
}

func (r *recordingGitHubClient) ListIssueComments(_ context.Context, repo string, issueNumber int) ([]ghapi.Comment, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	comments := make([]ghapi.Comment, 0, len(r.issueComments))
	for _, comment := range r.issueComments {
		if comment.repo != repo || comment.issueNumber != issueNumber {
			continue
		}
		comments = append(comments, ghapi.Comment{Body: comment.body})
	}
	return comments, nil
}

func (r *recordingGitHubClient) commentCalls() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.createIssueCommentCalls
}

func (r *recordingGitHubClient) comments() []testIssueComment {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]testIssueComment, len(r.issueComments))
	copy(out, r.issueComments)
	return out
}

func (r *recordingGitHubClient) reactions() []testIssueReaction {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]testIssueReaction, len(r.issueReactions))
	copy(out, r.issueReactions)
	return out
}

func TestGitHubRunNotifierNotifyRunStartedIdempotent(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	startedAt := time.Now().UTC()
	run := state.Run{
		ID:           "run_start",
		TaskID:       "owner/repo#42",
		Repo:         "owner/repo",
		RunDir:       runDir,
		IssueNumber:  42,
		Trigger:      runtrigger.NameIssueLabel,
		AgentRuntime: runtime.RuntimeCodex,
		CreatedAt:    startedAt.Add(-2 * time.Second),
		StartedAt:    &startedAt,
	}
	if _, err := store.UpsertTask(state.UpsertTaskInput{ID: run.TaskID, Repo: run.Repo, IssueNumber: run.IssueNumber}); err != nil {
		t.Fatalf("UpsertTask(start): %v", err)
	}
	if _, err := store.AddRun(state.CreateRunInput{
		ID:           run.ID,
		TaskID:       run.TaskID,
		Repo:         run.Repo,
		Instruction:  "Investigate issue #42",
		AgentRuntime: run.AgentRuntime,
		Trigger:      run.Trigger,
		RunDir:       run.RunDir,
		IssueNumber:  run.IssueNumber,
	}); err != nil {
		t.Fatalf("AddRun(start): %v", err)
	}
	if err := store.UpsertRunResponseTarget(state.RunResponseTargetRecord{
		RunID:       run.ID,
		Repo:        "owner/repo",
		IssueNumber: 42,
		Trigger:     runtrigger.NameIssueLabel,
	}); err != nil {
		t.Fatalf("UpsertRunResponseTarget(start): %v", err)
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
	if err := store.UpsertRunResponseTarget(state.RunResponseTargetRecord{
		RunID:       run.ID,
		Repo:        "owner/repo",
		IssueNumber: 77,
		RequestedBy: "rtzll",
		Trigger:     runtrigger.NamePRComment,
	}); err != nil {
		t.Fatalf("UpsertRunResponseTarget(completion): %v", err)
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

func TestGitHubRunNotifierNotifyRunStartedRetriesAfterPostFailure(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{
		createIssueCommentErrSeq: []error{
			errors.New("transient github failure"),
			nil,
		},
	}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 86,
		RequestedBy: "alice",
		Trigger:     runtrigger.NameIssueLabel,
	})
	startedAt := time.Now().UTC()
	run := state.Run{
		ID:           "run_start_comment_retry",
		TaskID:       "owner/repo#86",
		Repo:         "owner/repo",
		Instruction:  "Investigate issue #86",
		BaseBranch:   "main",
		HeadBranch:   "rascal/issue-86",
		Trigger:      runtrigger.NameIssueLabel,
		RunDir:       runDir,
		IssueNumber:  86,
		AgentRuntime: runtime.RuntimeCodex,
		StartedAt:    &startedAt,
	}

	notifier.NotifyRunStarted(run, runtime.SessionModeAll, false)

	if got := gh.commentCalls(); got != 1 {
		t.Fatalf("comment calls after first attempt = %d, want 1", got)
	}
	if comments := gh.comments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindStart); err != nil {
		t.Fatalf("GetRunNotification(start): %v", err)
	} else if ok {
		t.Fatal("expected start notification record to be absent after failed post")
	}

	notifier.NotifyRunStarted(run, runtime.SessionModeAll, false)

	if got := gh.commentCalls(); got != 2 {
		t.Fatalf("comment calls after retry = %d, want 2", got)
	}
	if comments := gh.comments(); len(comments) != 1 {
		t.Fatalf("expected one posted start comment after retry, got %d", len(comments))
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindStart); err != nil {
		t.Fatalf("GetRunNotification(start): %v", err)
	} else if !ok {
		t.Fatal("expected start notification record after successful retry")
	}
}

func TestGitHubRunNotifierNotifyRunStartedIncludesRunnerBuildCommit(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 85,
		RequestedBy: "alice",
		Trigger:     runtrigger.NameIssueLabel,
	})
	if err := runner.WriteMeta(filepath.Join(runDir, "meta.json"), runner.Meta{
		RunID:       "run_start_comment_commit",
		TaskID:      "owner/repo#85",
		Repo:        "owner/repo",
		BaseBranch:  "main",
		HeadBranch:  "rascal/issue-85",
		BuildCommit: "deadbee",
		ExitCode:    1,
	}); err != nil {
		t.Fatalf("write meta: %v", err)
	}
	startedAt := time.Now().UTC()
	run := state.Run{
		ID:           "run_start_comment_commit",
		TaskID:       "owner/repo#85",
		Repo:         "owner/repo",
		Instruction:  "Investigate issue #85",
		BaseBranch:   "main",
		HeadBranch:   "rascal/issue-85",
		Trigger:      runtrigger.NameIssueLabel,
		RunDir:       runDir,
		IssueNumber:  85,
		AgentRuntime: runtime.RuntimeCodex,
		StartedAt:    &startedAt,
	}

	notifier.NotifyRunStarted(run, runtime.SessionModeAll, false)

	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted start comment, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, "- Runner commit: `deadbee`") {
		t.Fatalf("expected runner commit in start comment, got:\n%s", comments[0].body)
	}
}

func TestGitHubRunNotifierNotifyRunCompletedRetriesAfterPostFailure(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{
		createIssueCommentErrSeq: []error{
			errors.New("transient github failure"),
			nil,
		},
	}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 89,
		RequestedBy: "alice",
		Trigger:     runtrigger.NamePRComment,
	})
	run, err := store.AddRun(state.CreateRunInput{
		ID:           "run_comment_retry",
		TaskID:       "owner/repo#89",
		Repo:         "owner/repo",
		Instruction:  "Address PR #89 feedback",
		BaseBranch:   "main",
		HeadBranch:   "rascal/pr-89",
		Trigger:      runtrigger.NamePRComment,
		RunDir:       runDir,
		PRNumber:     89,
		AgentRuntime: runtime.RuntimeGooseCodex,
	})
	if err != nil {
		t.Fatalf("store.AddRun: %v", err)
	}

	notifier.NotifyRunCompleted(run)

	if got := gh.commentCalls(); got != 1 {
		t.Fatalf("comment calls after first attempt = %d, want 1", got)
	}
	if comments := gh.comments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	persisted, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("store.GetRun(%q) not found", run.ID)
	}
	if persisted.CompletionCommentState != state.CompletionCommentStateFailed {
		t.Fatalf("completion comment state after failure = %q, want %q", persisted.CompletionCommentState, state.CompletionCommentStateFailed)
	}

	notifier.NotifyRunCompleted(run)

	if got := gh.commentCalls(); got != 2 {
		t.Fatalf("comment calls after retry = %d, want 2", got)
	}
	if comments := gh.comments(); len(comments) != 1 {
		t.Fatalf("expected one posted comment after retry, got %d", len(comments))
	}
	persisted, ok = store.GetRun(run.ID)
	if !ok {
		t.Fatalf("store.GetRun(%q) not found", run.ID)
	}
	if persisted.CompletionCommentState != state.CompletionCommentStatePosted {
		t.Fatalf("completion comment state after retry = %q, want %q", persisted.CompletionCommentState, state.CompletionCommentStatePosted)
	}
}

func TestGitHubRunNotifierNotifyRunCompletedReconcilesAmbiguousPostFailure(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{
		createIssueCommentErrSeq:          []error{errors.New("timeout after post")},
		createIssueCommentPostsOnErrorSeq: []bool{true},
	}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 91,
		RequestedBy: "alice",
		Trigger:     runtrigger.NamePRComment,
	})
	run, err := store.AddRun(state.CreateRunInput{
		ID:           "run_comment_ambiguous",
		TaskID:       "owner/repo#91",
		Repo:         "owner/repo",
		Instruction:  "Address PR #91 feedback",
		BaseBranch:   "main",
		HeadBranch:   "rascal/pr-91",
		Trigger:      runtrigger.NamePRComment,
		RunDir:       runDir,
		PRNumber:     91,
		AgentRuntime: runtime.RuntimeGooseCodex,
	})
	if err != nil {
		t.Fatalf("store.AddRun: %v", err)
	}

	notifier.NotifyRunCompleted(run)
	notifier.NotifyRunCompleted(run)

	if got := gh.commentCalls(); got != 1 {
		t.Fatalf("comment calls after ambiguous failure reconciliation = %d, want 1", got)
	}
	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted comment after ambiguous failure, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, runCompletionCommentToken(run.ID)) {
		t.Fatalf("completion comment missing run token:\n%s", comments[0].body)
	}
	persisted, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("store.GetRun(%q) not found", run.ID)
	}
	if persisted.CompletionCommentState != state.CompletionCommentStatePosted {
		t.Fatalf("completion comment state = %q, want %q", persisted.CompletionCommentState, state.CompletionCommentStatePosted)
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindCompletion); err != nil {
		t.Fatalf("GetRunNotification(completion): %v", err)
	} else if !ok {
		t.Fatal("expected completion notification record after ambiguous failure reconciliation")
	}
}

func TestGitHubRunNotifierNotifyRunFailedRetriesAfterPostFailure(t *testing.T) {
	store := newNotifierTestStore(t)
	gh := &recordingGitHubClient{
		createIssueCommentErrSeq: []error{
			errors.New("transient github failure"),
			nil,
		},
	}
	notifier := NewGitHubRunNotifier(config.ServerConfig{GitHubToken: "token"}, store, gh)

	runDir := t.TempDir()
	writeNotifierJSON(t, filepath.Join(runDir, RunResponseTargetFile), RunResponseTarget{
		Repo:        "owner/repo",
		IssueNumber: 90,
		RequestedBy: "alice",
		Trigger:     runtrigger.NamePRComment,
	})
	run := state.Run{
		ID:           "run_failure_retry",
		TaskID:       "owner/repo#90",
		Repo:         "owner/repo",
		Instruction:  "Address PR #90 feedback",
		BaseBranch:   "main",
		HeadBranch:   "rascal/pr-90",
		Trigger:      runtrigger.NamePRComment,
		RunDir:       runDir,
		PRNumber:     90,
		AgentRuntime: runtime.RuntimeCodex,
		Error:        "goose run failed: exit status 1",
	}

	notifier.NotifyRunFailed(run)

	if got := gh.commentCalls(); got != 1 {
		t.Fatalf("comment calls after first attempt = %d, want 1", got)
	}
	if comments := gh.comments(); len(comments) != 0 {
		t.Fatalf("expected no posted comments after failed attempt, got %d", len(comments))
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindFailure); err != nil {
		t.Fatalf("GetRunNotification(failure): %v", err)
	} else if ok {
		t.Fatal("expected failure notification record to be absent after failed post")
	}

	notifier.NotifyRunFailed(run)

	if got := gh.commentCalls(); got != 2 {
		t.Fatalf("comment calls after retry = %d, want 2", got)
	}
	comments := gh.comments()
	if len(comments) != 1 {
		t.Fatalf("expected one posted comment after retry, got %d", len(comments))
	}
	if !strings.Contains(comments[0].body, "Reason: goose run failed: exit status 1") {
		t.Fatalf("expected generic failure reason in comment, got body:\n%s", comments[0].body)
	}
	if _, ok, err := store.GetRunNotification(run.ID, state.RunNotificationKindFailure); err != nil {
		t.Fatalf("GetRunNotification(failure): %v", err)
	} else if !ok {
		t.Fatal("expected failure notification record after successful retry")
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
