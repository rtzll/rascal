package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/rtzll/rascal/internal/api"
	ghapi "github.com/rtzll/rascal/internal/github"
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
	switch eventType {
	case "issues":
		var ev ghapi.IssuesEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode issues event: %w", err)
		}
		if ev.Issue.PullRequest != nil {
			return nil
		}
		if s.isBotActor(ev.Sender.Login) {
			return nil
		}
		switch ev.Action {
		case "labeled":
			if !strings.EqualFold(ev.Label.Name, "rascal") {
				return nil
			}
			taskID := repoIssueTaskID(ev.Repository.FullName, ev.Issue.Number)
			_, err := s.CreateAndQueueRun(RunRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Instruction: ghapi.IssueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     runtrigger.NameIssueLabel,
				IssueNumber: ev.Issue.Number,
				Context:     fmt.Sprintf("Triggered by label 'rascal' on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
				ResponseTarget: &RunResponseTarget{
					Repo:        ev.Repository.FullName,
					IssueNumber: ev.Issue.Number,
					RequestedBy: strings.TrimSpace(ev.Sender.Login),
					Trigger:     runtrigger.NameIssueLabel,
				},
			})
			if errors.Is(err, errTaskCompleted) {
				return nil
			}
			return err
		case "unlabeled":
			if !strings.EqualFold(ev.Label.Name, "rascal") {
				return nil
			}
			s.removeIssueReactionsBestEffort(ev.Repository.FullName, ev.Issue.Number)
			return nil
		case "edited":
			if !ghapi.IssueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := repoIssueTaskID(ev.Repository.FullName, ev.Issue.Number)
			if err := s.Store.CancelQueuedRuns(taskID, "issue edited"); err != nil {
				return fmt.Errorf("cancel queued runs for edited issue: %w", err)
			}
			_, err := s.CreateAndQueueRun(RunRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Instruction: ghapi.IssueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     runtrigger.NameIssueEdited,
				IssueNumber: ev.Issue.Number,
				Context:     fmt.Sprintf("Triggered by issue edit on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
				ResponseTarget: &RunResponseTarget{
					Repo:        ev.Repository.FullName,
					IssueNumber: ev.Issue.Number,
					RequestedBy: strings.TrimSpace(ev.Sender.Login),
					Trigger:     runtrigger.NameIssueEdited,
				},
			})
			if errors.Is(err, errTaskCompleted) {
				return nil
			}
			return err
		case "closed":
			if !ghapi.IssueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := repoIssueTaskID(ev.Repository.FullName, ev.Issue.Number)
			if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
				ID:          taskID,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			}); err != nil {
				return fmt.Errorf("upsert task for closed issue: %w", err)
			}
			if err := s.Store.MarkTaskCompleted(taskID); err != nil {
				return fmt.Errorf("mark task completed for closed issue: %w", err)
			}
			if err := s.Store.CancelQueuedRuns(taskID, "issue closed"); err != nil {
				return fmt.Errorf("cancel queued runs for closed issue: %w", err)
			}
			s.cancelRunningTaskRuns(taskID, "issue closed")
			return nil
		case "reopened":
			if !ghapi.IssueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := repoIssueTaskID(ev.Repository.FullName, ev.Issue.Number)
			if _, err := s.Store.UpsertTask(state.UpsertTaskInput{
				ID:          taskID,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			}); err != nil {
				return fmt.Errorf("upsert task for reopened issue: %w", err)
			}
			if err := s.Store.MarkTaskOpen(taskID); err != nil {
				return fmt.Errorf("mark task open for reopened issue: %w", err)
			}
			_, err := s.CreateAndQueueRun(RunRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Instruction: ghapi.IssueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     runtrigger.NameIssueReopened,
				IssueNumber: ev.Issue.Number,
				PRStatus:    state.PRStatusNone,
				Context:     fmt.Sprintf("Triggered by issue reopen on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
				ResponseTarget: &RunResponseTarget{
					Repo:        ev.Repository.FullName,
					IssueNumber: ev.Issue.Number,
					RequestedBy: strings.TrimSpace(ev.Sender.Login),
					Trigger:     runtrigger.NameIssueReopened,
				},
			})
			if errors.Is(err, errTaskCompleted) {
				return nil
			}
			return err
		default:
			return nil
		}
	case "issue_comment":
		var ev ghapi.IssueCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode issue_comment event: %w", err)
		}
		if ev.Issue.PullRequest == nil {
			return nil
		}
		switch ev.Action {
		case "created":
		case "edited":
			if !ghapi.IssueCommentBodyChanged(ev) {
				return nil
			}
		default:
			return nil
		}
		if ghapi.IsAutomationComment(ev.Comment.Body, runCompletionCommentBodyMarker, runStartCommentBodyMarker, runFailureCommentBodyMarker) {
			return nil
		}
		if s.isBotActor(ev.Comment.User.Login) || s.isBotActor(ev.Sender.Login) {
			return nil
		}
		task, ok := s.activeTaskForPR(ev.Repository.FullName, ev.Issue.Number)
		if !ok {
			return nil
		}
		s.addIssueCommentReactionBestEffort(ev.Repository.FullName, ev.Comment.ID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, ev.Repository.FullName, ev.Issue.Number, "", "")

		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Instruction: fmt.Sprintf("Address PR #%d feedback", ev.Issue.Number),
			Trigger:     runtrigger.NamePRComment,
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.Issue.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     strings.TrimSpace(ev.Comment.Body),
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     runtrigger.NamePRComment,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case "pull_request_review":
		var ev ghapi.PullRequestReviewEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode pull_request_review event: %w", err)
		}
		if ev.Action != "submitted" {
			return nil
		}
		if s.isBotActor(ev.Review.User.Login) || s.isBotActor(ev.Sender.Login) {
			return nil
		}
		task, ok := s.activeTaskForPR(ev.Repository.FullName, ev.PullRequest.Number)
		if !ok {
			return nil
		}
		s.addPullRequestReviewReactionBestEffort(ev.Repository.FullName, ev.PullRequest.Number, ev.Review.ID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, ev.Repository.FullName, ev.PullRequest.Number, ev.PullRequest.Base.Ref, ev.PullRequest.Head.Ref)

		contextText := strings.TrimSpace(ev.Review.Body)
		if contextText == "" {
			contextText = fmt.Sprintf("review state: %s", ev.Review.State)
		}
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Instruction: fmt.Sprintf("Address PR #%d review feedback", ev.PullRequest.Number),
			Trigger:     runtrigger.NamePRReview,
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.PullRequest.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     contextText,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.PullRequest.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     runtrigger.NamePRReview,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case "pull_request_review_comment":
		var ev ghapi.PullRequestReviewCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode pull_request_review_comment event: %w", err)
		}
		switch ev.Action {
		case "created":
		case "edited":
			if !ghapi.ReviewCommentBodyChanged(ev) {
				return nil
			}
		default:
			return nil
		}
		if s.isBotActor(ev.Comment.User.Login) || s.isBotActor(ev.Sender.Login) {
			return nil
		}
		task, ok := s.activeTaskForPR(ev.Repository.FullName, ev.PullRequest.Number)
		if !ok {
			return nil
		}
		s.addPullRequestReviewCommentReactionBestEffort(ev.Repository.FullName, ev.Comment.ID, ghapi.ReactionEyes)
		baseBranch, headBranch := s.resolvePRBranches(ctx, ev.Repository.FullName, ev.PullRequest.Number, ev.PullRequest.Base.Ref, ev.PullRequest.Head.Ref)

		contextText := strings.TrimSpace(ev.Comment.Body)
		if location := ghapi.FormatReviewCommentLocation(ev.Comment.Path, ev.Comment.StartLine, ev.Comment.Line); location != "" {
			if contextText == "" {
				contextText = fmt.Sprintf("inline review comment at %s", location)
			} else {
				contextText = fmt.Sprintf(`%s

Inline comment location: %s`, contextText, location)
			}
		}
		_, err := s.CreateAndQueueRun(RunRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Instruction: fmt.Sprintf("Address PR #%d inline review comment", ev.PullRequest.Number),
			Trigger:     runtrigger.NamePRReviewComment,
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.PullRequest.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     contextText,
			BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
			HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
			Debug:       boolPtr(true),
			ResponseTarget: &RunResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.PullRequest.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     runtrigger.NamePRReviewComment,
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
	case "pull_request_review_thread":
		var ev ghapi.PullRequestReviewThreadEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode pull_request_review_thread event: %w", err)
		}
		switch ev.Action {
		case "unresolved":
			if s.isBotActor(ev.Sender.Login) {
				return nil
			}
			task, ok := s.activeTaskForPR(ev.Repository.FullName, ev.PullRequest.Number)
			if !ok {
				return nil
			}
			baseBranch, headBranch := s.resolvePRBranches(ctx, ev.Repository.FullName, ev.PullRequest.Number, ev.PullRequest.Base.Ref, ev.PullRequest.Head.Ref)
			_, err := s.CreateAndQueueRun(RunRequest{
				TaskID:      task.ID,
				Repo:        ev.Repository.FullName,
				Instruction: fmt.Sprintf("Address PR #%d unresolved review thread", ev.PullRequest.Number),
				Trigger:     runtrigger.NamePRReviewThread,
				IssueNumber: task.IssueNumber,
				PRNumber:    ev.PullRequest.Number,
				PRStatus:    state.PRStatusOpen,
				Context:     ghapi.ReviewThreadContext(ev.Thread),
				BaseBranch:  s.firstKnownBaseBranch(task.ID, baseBranch),
				HeadBranch:  s.firstKnownHeadBranch(task.ID, headBranch),
				Debug:       boolPtr(true),
				ResponseTarget: &RunResponseTarget{
					Repo:           ev.Repository.FullName,
					IssueNumber:    ev.PullRequest.Number,
					RequestedBy:    strings.TrimSpace(ev.Sender.Login),
					Trigger:        runtrigger.NamePRReviewThread,
					ReviewThreadID: ev.Thread.ID,
				},
			})
			if errors.Is(err, errTaskCompleted) {
				return nil
			}
			return err
		case "resolved":
			task, ok := s.taskForPR(ev.Repository.FullName, ev.PullRequest.Number)
			if !ok {
				return nil
			}
			s.cancelQueuedReviewThreadRuns(task.ID, ev.Repository.FullName, ev.PullRequest.Number, ev.Thread.ID, "review thread resolved")
			return nil
		default:
			return nil
		}
	case "pull_request":
		var ev ghapi.PullRequestEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode pull_request event: %w", err)
		}
		task, ok := s.taskForPR(ev.Repository.FullName, ev.PullRequest.Number)
		if !ok {
			return nil
		}
		if ev.Action == "closed" {
			taskID := task.ID
			if ev.PullRequest.Merged {
				if err := s.Store.MarkTaskCompleted(taskID); err != nil {
					return fmt.Errorf("mark task completed for merged PR: %w", err)
				}
				if err := s.Store.CancelQueuedRuns(taskID, "task completed by merged PR"); err != nil {
					return fmt.Errorf("cancel queued runs for merged PR: %w", err)
				}
				s.reconcileClosedPRRuns(ev.Repository.FullName, ev.PullRequest.Number, true)
				s.addIssueReactionBestEffort(ev.Repository.FullName, ev.PullRequest.Number, ghapi.ReactionRocket)
			} else {
				s.reconcileClosedPRRuns(ev.Repository.FullName, ev.PullRequest.Number, false)
				s.addIssueReactionBestEffort(ev.Repository.FullName, ev.PullRequest.Number, ghapi.ReactionMinusOne)
			}
		}
		if ev.Action == "reopened" {
			s.reconcileReopenedPRRuns(ev.Repository.FullName, ev.PullRequest.Number)
		}
		return nil
	default:
		return nil
	}
}

func (s *Server) isBotActor(login string) bool {
	login = strings.TrimSpace(strings.ToLower(login))
	if login == "" {
		return false
	}
	if strings.TrimSpace(s.Config.BotLogin) != "" && login == strings.ToLower(strings.TrimSpace(s.Config.BotLogin)) {
		return true
	}
	return strings.Contains(login, "[bot]")
}
