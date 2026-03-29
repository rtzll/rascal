package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rtzll/rascal/internal/api"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) HandleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}
	if !s.isActiveWebhookSlot() {
		accepted := false
		writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted, InactiveSlot: true})
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if secret := strings.TrimSpace(s.Config.GitHubWebhookSecret); secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !ghapi.VerifySignatureSHA256([]byte(secret), payload, sig) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
	}

	deliveryID := ghapi.DeliveryID(r.Header)
	var deliveryClaim state.DeliveryClaim
	if deliveryID != "" {
		claim, claimed, claimErr := s.Store.ClaimDelivery(deliveryID, s.InstanceID)
		if claimErr != nil {
			http.Error(w, "failed to claim delivery id", http.StatusInternalServerError)
			return
		}
		if !claimed {
			writeJSON(w, http.StatusOK, api.AcceptedResponse{Duplicate: true})
			return
		}
		deliveryClaim = claim
	}

	eventType := ghapi.EventType(r.Header)
	if err := s.processWebhookEvent(r.Context(), eventType, payload); err != nil {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "webhook processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if deliveryClaim.ID != "" {
		if err := s.Store.CompleteDeliveryClaim(deliveryClaim); err != nil {
			http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
			return
		}
	}

	accepted := true
	writeJSON(w, http.StatusAccepted, api.AcceptedResponse{Accepted: &accepted})
}

func (s *Server) processWebhookEvent(ctx context.Context, eventType string, payload []byte) error {
	actions, err := NewWebhookInterpreter(s.Config.BotLogin).Interpret(eventType, payload)
	if err != nil {
		return err
	}
	for _, action := range actions {
		if err := s.executeWebhookAction(ctx, action); err != nil {
			return err
		}
	}
	return nil
}

func (s *Server) executeWebhookAction(ctx context.Context, action WebhookAction) error {
	switch action.Kind {
	case WebhookActionCreateIssueRun:
		if action.TaskID == "" {
			return nil
		}
		if action.Trigger == runtrigger.NameIssueLabel {
			if _, active := s.Store.ActiveRunForTask(action.TaskID); active {
				return nil
			}
		}
		agentRuntime, err := s.runtimeFromIssueLabels(ctx, action.Repo, action.IssueNumber, action.Labels)
		if err != nil {
			return err
		}
		_, err = s.CreateAndQueueRun(RunRequest{
			TaskID:       action.TaskID,
			Repo:         action.Repo,
			Instruction:  action.Instruction,
			AgentRuntime: agentRuntime,
			Trigger:      action.Trigger,
			IssueNumber:  action.IssueNumber,
			PRStatus:     state.PRStatusNone,
			Context:      action.Context,
			Debug:        boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        action.Repo,
				IssueNumber: action.IssueNumber,
				RequestedBy: action.RequestedBy,
				Trigger:     action.Trigger,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case WebhookActionCancelTaskRuns:
		if action.TaskID == "" {
			return nil
		}
		if err := s.Store.CancelQueuedRuns(action.TaskID, action.CancelReason, action.StatusReason); err != nil {
			return fmt.Errorf("cancel queued runs for task %s: %w", action.TaskID, err)
		}
		s.cancelRunningTaskRuns(action.TaskID, action.CancelReason, action.StatusReason)
		return nil
	case WebhookActionClearIssueReactions:
		s.notifier().ClearIssueReactions(action.Repo, action.IssueNumber)
		return nil
	case WebhookActionCompleteIssueTask:
		if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
			ID:          action.TaskID,
			Repo:        action.Repo,
			IssueNumber: action.IssueNumber,
		}); err != nil {
			return fmt.Errorf("upsert task for closed issue: %w", err)
		}
		if err := s.Store.MarkTaskCompleted(action.TaskID); err != nil {
			return fmt.Errorf("mark task completed for closed issue: %w", err)
		}
		if err := s.Store.CancelQueuedRuns(action.TaskID, "issue closed", state.RunStatusReasonIssueClosed); err != nil {
			return fmt.Errorf("cancel queued runs for closed issue: %w", err)
		}
		s.cancelRunningTaskRuns(action.TaskID, "issue closed", state.RunStatusReasonIssueClosed)
		return nil
	case WebhookActionReopenIssueTask:
		if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
			ID:          action.TaskID,
			Repo:        action.Repo,
			IssueNumber: action.IssueNumber,
		}); err != nil {
			return fmt.Errorf("upsert task for reopened issue: %w", err)
		}
		if err := s.Store.MarkTaskOpen(action.TaskID); err != nil {
			return fmt.Errorf("mark task open for reopened issue: %w", err)
		}
		return nil
	case WebhookActionCreatePRCommentRun:
		task, ok := s.activeTaskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		s.notifier().ReactToIssueComment(action.Repo, action.CommentID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, action.Repo, action.PRNumber, action.BaseBranch, action.HeadBranch)
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        action.Repo,
			Instruction: action.Instruction,
			Trigger:     action.Trigger,
			IssueNumber: task.IssueNumber,
			PRNumber:    action.PRNumber,
			PRStatus:    state.PRStatusOpen,
			Context:     action.Context,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        action.Repo,
				IssueNumber: action.PRNumber,
				RequestedBy: action.RequestedBy,
				Trigger:     action.Trigger,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case WebhookActionCreatePRReviewRun:
		task, ok := s.activeTaskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		s.notifier().ReactToPullRequestReview(action.Repo, action.PRNumber, action.ReviewID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, action.Repo, action.PRNumber, action.BaseBranch, action.HeadBranch)
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        action.Repo,
			Instruction: action.Instruction,
			Trigger:     action.Trigger,
			IssueNumber: task.IssueNumber,
			PRNumber:    action.PRNumber,
			PRStatus:    state.PRStatusOpen,
			Context:     action.Context,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        action.Repo,
				IssueNumber: action.PRNumber,
				RequestedBy: action.RequestedBy,
				Trigger:     action.Trigger,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case WebhookActionCreatePRReviewCommentRun:
		task, ok := s.activeTaskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		s.notifier().ReactToPullRequestReviewComment(action.Repo, action.CommentID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, action.Repo, action.PRNumber, action.BaseBranch, action.HeadBranch)
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        action.Repo,
			Instruction: action.Instruction,
			Trigger:     action.Trigger,
			IssueNumber: task.IssueNumber,
			PRNumber:    action.PRNumber,
			PRStatus:    state.PRStatusOpen,
			Context:     action.Context,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        action.Repo,
				IssueNumber: action.PRNumber,
				RequestedBy: action.RequestedBy,
				Trigger:     action.Trigger,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case WebhookActionCreatePRThreadRun:
		task, ok := s.activeTaskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		baseBranch, headBranch := s.resolvePRBranches(ctx, action.Repo, action.PRNumber, action.BaseBranch, action.HeadBranch)
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        action.Repo,
			Instruction: action.Instruction,
			Trigger:     action.Trigger,
			IssueNumber: task.IssueNumber,
			PRNumber:    action.PRNumber,
			PRStatus:    state.PRStatusOpen,
			Context:     action.Context,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:           action.Repo,
				IssueNumber:    action.PRNumber,
				RequestedBy:    action.RequestedBy,
				Trigger:        action.Trigger,
				ReviewThreadID: action.ReviewThreadID,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case WebhookActionCancelPRThreadRuns:
		task, ok := s.taskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		s.cancelQueuedReviewThreadRuns(task.ID, action.Repo, action.PRNumber, action.ReviewThreadID, action.CancelReason)
		return nil
	case WebhookActionClosePullRequest:
		task, ok := s.taskForPR(action.Repo, action.PRNumber)
		if !ok {
			return nil
		}
		if action.Merged {
			if err := s.Store.MarkTaskCompleted(task.ID); err != nil {
				return fmt.Errorf("mark task completed for merged PR: %w", err)
			}
			if err := s.Store.CancelQueuedRuns(task.ID, "pull request merged", state.RunStatusReasonPRMerged); err != nil {
				return fmt.Errorf("cancel queued runs for merged PR: %w", err)
			}
			s.cancelRunningTaskRuns(task.ID, "pull request merged", state.RunStatusReasonPRMerged)
			s.reconcileClosedPRRuns(action.Repo, action.PRNumber, true)
			s.notifier().ReactToIssue(action.Repo, action.PRNumber, ghapi.ReactionRocket)
			return nil
		}
		if err := s.Store.CancelQueuedRuns(task.ID, "pull request closed", state.RunStatusReasonPRClosed); err != nil {
			return fmt.Errorf("cancel queued runs for closed PR: %w", err)
		}
		s.cancelRunningTaskRuns(task.ID, "pull request closed", state.RunStatusReasonPRClosed)
		s.reconcileClosedPRRuns(action.Repo, action.PRNumber, false)
		s.notifier().ReactToIssue(action.Repo, action.PRNumber, ghapi.ReactionMinusOne)
		return nil
	case WebhookActionReopenPullRequest:
		s.reconcileReopenedPRRuns(action.Repo, action.PRNumber)
		return nil
	default:
		return nil
	}
}

func isOpenGitHubState(raw string) bool {
	raw = strings.ToLower(strings.TrimSpace(raw))
	return raw == "" || raw == "open"
}

// runtimeFromIssueLabels resolves the agent runtime from issue labels.
// Returns nil (use server default) if no runtime label is present.
// Posts a comment and returns an error if a label has an unrecognized runtime suffix.
func (s *Server) runtimeFromIssueLabels(ctx context.Context, repo string, issueNumber int, labels []ghapi.Label) (*runtime.Runtime, error) {
	names := ghapi.LabelNames(labels)
	rt, ok, err := runtime.RuntimeFromLabels(names)
	if err != nil {
		_ = ctx
		s.notifier().NotifyInvalidRuntimeLabel(repo, issueNumber, err)
		return nil, fmt.Errorf("unknown runtime label on %s#%d: %w", repo, issueNumber, err)
	}
	if !ok {
		return nil, nil
	}
	return &rt, nil
}
