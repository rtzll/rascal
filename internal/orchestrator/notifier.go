package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

type GitHubRunNotifier struct {
	Config config.ServerConfig
	Store  *state.Store
	GitHub GitHubClient
}

const completionCommentClaimTimeout = 5 * time.Minute

func NewGitHubRunNotifier(cfg config.ServerConfig, store *state.Store, gh GitHubClient) *GitHubRunNotifier {
	return &GitHubRunNotifier{
		Config: cfg,
		Store:  store,
		GitHub: gh,
	}
}

func (n *GitHubRunNotifier) NotifyRunStarted(run state.Run, sessionMode runtime.SessionMode, sessionResume bool) {
	if !n.enabled() {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if posted, err := n.notificationAlreadyPosted(run.ID, state.RunNotificationKindStart); err != nil {
		log.Printf("failed to check start notification record for run %s: %v", run.ID, err)
		return
	} else if posted {
		return
	}
	if markerExists, err := runStartCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check start comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	runCredentialInfo, _ := n.Store.GetRunCredentialInfo(run.ID)
	body := buildRunStartComment(run, target, requesterForRun(run, target, runCredentialInfo.CreatedByUserID), sessionMode, sessionResume)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post start comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := n.Store.UpsertRunNotification(state.RunNotification{
		RunID:       run.ID,
		Kind:        state.RunNotificationKindStart,
		Repo:        repo,
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC(),
	}); err != nil {
		log.Printf("failed to persist start notification for run %s: %v", run.ID, err)
	}
}

func (n *GitHubRunNotifier) NotifyRunCompleted(run state.Run) {
	if !isCommentTriggeredRun(run.Trigger) || !n.enabled() {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
		return
	}
	if !ok {
		return
	}

	repo := strings.TrimSpace(target.Repo)
	if repo == "" {
		repo = strings.TrimSpace(run.Repo)
	}
	issueNumber := target.IssueNumber
	if issueNumber <= 0 {
		issueNumber = run.PRNumber
	}
	if repo == "" || issueNumber <= 0 {
		return
	}
	if posted, err := n.notificationAlreadyPosted(run.ID, state.RunNotificationKindCompletion); err != nil {
		log.Printf("failed to check completion notification record for run %s: %v", run.ID, err)
		return
	} else if posted {
		return
	}

	claimedRun, claimed, err := n.Store.ClaimRunCompletionComment(run.ID, n.completionCommentClaimOwner(), time.Now().UTC().Add(-completionCommentClaimTimeout))
	if err != nil {
		log.Printf("failed to claim completion comment for run %s: %v", run.ID, err)
		return
	}
	if !claimed {
		return
	}
	run = claimedRun

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	posted, err := n.completionCommentAlreadyPosted(ctx, repo, issueNumber, run.ID)
	if err != nil {
		if markErr := n.Store.MarkRunCompletionCommentFailed(run.ID, err.Error()); markErr != nil {
			log.Printf("failed to mark completion comment failure for run %s: %v", run.ID, markErr)
		}
		log.Printf("failed to check completion comment token for run %s: %v", run.ID, err)
		return
	}
	if posted {
		if err := n.Store.UpsertRunNotification(state.RunNotification{
			RunID:       run.ID,
			Kind:        state.RunNotificationKindCompletion,
			Repo:        repo,
			IssueNumber: issueNumber,
			PostedAt:    time.Now().UTC(),
		}); err != nil {
			log.Printf("failed to persist completion notification for run %s: %v", run.ID, err)
		}
		if err := n.Store.MarkRunCompletionCommentPosted(run.ID, time.Now().UTC()); err != nil {
			log.Printf("failed to mark completion comment posted for run %s: %v", run.ID, err)
		}
		return
	}

	var totalTokens *int64
	if usage, ok := n.Store.GetRunTokenUsage(run.ID); ok && usage.TotalTokens > 0 {
		totalTokens = &usage.TotalTokens
	}

	body, err := buildRunCompletionComment(run, target, repo, totalTokens)
	if err != nil {
		if markErr := n.Store.MarkRunCompletionCommentFailed(run.ID, err.Error()); markErr != nil {
			log.Printf("failed to mark completion comment failure for run %s: %v", run.ID, markErr)
		}
		log.Printf("failed to build completion comment for %s: %v", run.ID, err)
		return
	}

	if err := n.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		posted, postedErr := n.completionCommentAlreadyPosted(ctx, repo, issueNumber, run.ID)
		if postedErr == nil && posted {
			if err := n.Store.UpsertRunNotification(state.RunNotification{
				RunID:       run.ID,
				Kind:        state.RunNotificationKindCompletion,
				Repo:        repo,
				IssueNumber: issueNumber,
				PostedAt:    time.Now().UTC(),
			}); err != nil {
				log.Printf("failed to persist completion notification for run %s after ambiguous post: %v", run.ID, err)
			}
			if markErr := n.Store.MarkRunCompletionCommentPosted(run.ID, time.Now().UTC()); markErr != nil {
				log.Printf("failed to mark completion comment posted for run %s after ambiguous post: %v", run.ID, markErr)
			}
			return
		}
		if postedErr != nil {
			log.Printf("failed to reconcile ambiguous completion comment post for run %s: %v", run.ID, postedErr)
		}
		if markErr := n.Store.MarkRunCompletionCommentFailed(run.ID, err.Error()); markErr != nil {
			log.Printf("failed to mark completion comment failure for run %s: %v", run.ID, markErr)
		}
		log.Printf("failed to post completion comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := n.Store.UpsertRunNotification(state.RunNotification{
		RunID:       run.ID,
		Kind:        state.RunNotificationKindCompletion,
		Repo:        repo,
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC(),
	}); err != nil {
		log.Printf("failed to persist completion notification for run %s: %v", run.ID, err)
	}
	if err := n.Store.MarkRunCompletionCommentPosted(run.ID, time.Now().UTC()); err != nil {
		log.Printf("failed to mark completion comment posted for run %s: %v", run.ID, err)
	}
}

func (n *GitHubRunNotifier) NotifyRunFailed(run state.Run) {
	if !n.enabled() {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if posted, err := n.notificationAlreadyPosted(run.ID, state.RunNotificationKindFailure); err != nil {
		log.Printf("failed to check failure notification record for run %s: %v", run.ID, err)
		return
	} else if posted {
		return
	}
	if markerExists, err := runFailureCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check failure comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	body, err := buildRunFailureComment(run, target)
	if err != nil {
		log.Printf("failed to build failure comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := n.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post failure comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := n.Store.UpsertRunNotification(state.RunNotification{
		RunID:       run.ID,
		Kind:        state.RunNotificationKindFailure,
		Repo:        repo,
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC(),
	}); err != nil {
		log.Printf("failed to persist failure notification for run %s: %v", run.ID, err)
	}
}

func (n *GitHubRunNotifier) NotifyRunTerminal(run state.Run) {
	switch run.Status {
	case state.StatusSucceeded:
		n.ReactToIssue(run.Repo, run.IssueNumber, ghapi.ReactionRocket)
		n.NotifyRunCompleted(run)
	case state.StatusReview:
		n.ReactToIssue(run.Repo, run.IssueNumber, ghapi.ReactionHooray)
		n.NotifyRunCompleted(run)
	case state.StatusFailed:
		n.ReactToIssue(run.Repo, run.IssueNumber, ghapi.ReactionConfused)
		n.NotifyRunFailed(run)
	case state.StatusCanceled:
		n.ReactToIssue(run.Repo, run.IssueNumber, ghapi.ReactionMinusOne)
	}
}

func (n *GitHubRunNotifier) NotifyInvalidRuntimeLabel(repo string, issueNumber int, err error) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" || err == nil || !n.enabled() {
		return
	}
	msg := fmt.Sprintf("Unknown agent runtime in label. %s\n\nPlease use a valid runtime label (e.g. `rascal:claude`, `rascal:codex`, `rascal:goose-codex`, `rascal:goose-claude`).", err)
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if commentErr := n.GitHub.CreateIssueComment(ctx, repo, issueNumber, msg); commentErr != nil {
		log.Printf("failed to post unknown runtime comment on %s#%d: %v", repo, issueNumber, commentErr)
	}
}

func (n *GitHubRunNotifier) ReactToIssue(repo string, issueNumber int, reaction string) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" || !n.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.GitHub.AddIssueReaction(ctx, repo, issueNumber, reaction); err != nil {
		log.Printf("failed to add %q reaction for %s#%d: %v", reaction, repo, issueNumber, err)
	}
}

func (n *GitHubRunNotifier) ClearIssueReactions(repo string, issueNumber int) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" || !n.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.GitHub.RemoveIssueReactions(ctx, repo, issueNumber); err != nil {
		log.Printf("failed to remove reactions for %s#%d: %v", repo, issueNumber, err)
	}
}

func (n *GitHubRunNotifier) ReactToIssueComment(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" || !n.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.GitHub.AddIssueCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for issue comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func (n *GitHubRunNotifier) ReactToPullRequestReview(repo string, pullNumber int, reviewID int64, reaction string) {
	if reviewID <= 0 || pullNumber <= 0 || strings.TrimSpace(repo) == "" || !n.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.GitHub.AddPullRequestReviewReaction(ctx, repo, pullNumber, reviewID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review %d on %s#%d: %v", reaction, reviewID, repo, pullNumber, err)
	}
}

func (n *GitHubRunNotifier) ReactToPullRequestReviewComment(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" || !n.enabled() {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := n.GitHub.AddPullRequestReviewCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func (n *GitHubRunNotifier) enabled() bool {
	return strings.TrimSpace(n.Config.GitHubToken) != "" && n.GitHub != nil && n.Store != nil
}

func (n *GitHubRunNotifier) completionCommentAlreadyPosted(ctx context.Context, repo string, issueNumber int, runID string) (bool, error) {
	comments, err := n.GitHub.ListIssueComments(ctx, repo, issueNumber)
	if err != nil {
		return false, fmt.Errorf("list issue comments for %s#%d: %w", repo, issueNumber, err)
	}
	for _, comment := range comments {
		if hasRunCompletionCommentToken(comment.Body, runID) {
			return true, nil
		}
	}
	return false, nil
}

func (n *GitHubRunNotifier) completionCommentClaimOwner() string {
	if slot := strings.TrimSpace(n.Config.Slot); slot != "" {
		return slot
	}
	return "rascald"
}

func (n *GitHubRunNotifier) notificationAlreadyPosted(runID string, kind state.RunNotificationKind) (bool, error) {
	_, ok, err := n.Store.GetRunNotification(runID, kind)
	if err != nil {
		return false, fmt.Errorf("get %s notification for run %s: %w", kind, strings.TrimSpace(runID), err)
	}
	return ok, nil
}
