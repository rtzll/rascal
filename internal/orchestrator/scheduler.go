package orchestrator

import (
	"context"
	"fmt"
	"log"
	"strings"

	"github.com/rtzll/rascal/internal/state"
)

func (s *Server) ScheduleRuns(preferredTaskID string) {
	if s.Scheduler == nil {
		return
	}
	s.syncComponents()
	if err := s.Scheduler.Schedule(context.Background(), preferredTaskID); err != nil {
		log.Printf("schedule runs failed: %v", err)
	}
}

func (s *Server) reconcileClosedPRRuns(repo string, prNumber int, merged bool) {
	repo = state.NormalizeRepo(repo)
	if repo == "" || prNumber <= 0 {
		return
	}
	runs := s.Store.ListRuns(10000)
	for _, run := range runs {
		if !strings.EqualFold(run.Repo, repo) || run.PRNumber != prNumber {
			continue
		}
		if merged {
			if run.Status == state.StatusReview {
				if _, err := s.SM.TransitionBatch(run.ID, state.StatusSucceeded, func(r *state.Run) {
					r.PRStatus = state.PRStatusMerged
				}); err != nil {
					log.Printf("run %s reconcile PR merged (review→succeeded) failed: %v", run.ID, err)
				}
			} else {
				// Update PRStatus without changing run status.
				if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
					r.PRStatus = state.PRStatusMerged
					return nil
				}); err != nil {
					log.Printf("run %s update PR status to merged failed: %v", run.ID, err)
				}
			}
		} else {
			if run.PRStatus == state.PRStatusMerged {
				continue
			}
			if run.Status == state.StatusReview {
				if _, err := s.SM.TransitionBatch(run.ID, state.StatusCanceled, func(r *state.Run) {
					r.PRStatus = state.PRStatusClosedUnmerged
				}, WithError("pull request closed without merge"), WithReason(state.RunStatusReasonPRClosed)); err != nil {
					log.Printf("run %s reconcile PR closed (review→canceled) failed: %v", run.ID, err)
				}
			} else {
				// Update PRStatus without changing run status.
				if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
					r.PRStatus = state.PRStatusClosedUnmerged
					return nil
				}); err != nil {
					log.Printf("run %s update PR status to closed_unmerged failed: %v", run.ID, err)
				}
			}
		}
	}
}

func (s *Server) reconcileReopenedPRRuns(repo string, prNumber int) {
	repo = state.NormalizeRepo(repo)
	if repo == "" || prNumber <= 0 {
		return
	}
	runs := s.Store.ListRuns(10000)
	for _, run := range runs {
		if !strings.EqualFold(run.Repo, repo) || run.PRNumber != prNumber {
			continue
		}
		if run.PRStatus == state.PRStatusMerged {
			continue
		}
		if _, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
			if r.PRStatus == state.PRStatusMerged {
				return nil
			}
			r.PRStatus = state.PRStatusOpen
			return nil
		}); err != nil {
			log.Printf("run %s reconcile PR reopened failed: %v", run.ID, err)
		}
	}
}

func (s *Server) taskForPR(repo string, prNumber int) (state.Task, bool) {
	if state.NormalizeRepo(repo) == "" || prNumber <= 0 {
		return state.Task{}, false
	}
	return s.Store.FindTaskByPR(repo, prNumber)
}

func repoIssueTaskID(repo string, issueNumber int) string {
	repo = state.NormalizeRepo(repo)
	if repo == "" || issueNumber <= 0 {
		return ""
	}
	return fmt.Sprintf("%s#%d", repo, issueNumber)
}

func (s *Server) activeTaskForPR(repo string, prNumber int) (state.Task, bool) {
	task, ok := s.taskForPR(repo, prNumber)
	if !ok || task.Status != state.TaskOpen || task.PRDraft {
		return state.Task{}, false
	}
	return task, true
}

func (s *Server) taskForHeadBranch(repo, headBranch string) (state.Task, bool) {
	repo = state.NormalizeRepo(repo)
	headBranch = strings.TrimSpace(headBranch)
	if repo == "" || headBranch == "" {
		return state.Task{}, false
	}
	for _, run := range s.Store.ListRuns(10000) {
		if !strings.EqualFold(run.Repo, repo) || strings.TrimSpace(run.HeadBranch) != headBranch {
			continue
		}
		task, ok := s.Store.GetTask(run.TaskID)
		if ok {
			return task, true
		}
	}
	return state.Task{}, false
}

func (s *Server) activeTaskForHeadBranch(repo, headBranch string) (state.Task, bool) {
	task, ok := s.taskForHeadBranch(repo, headBranch)
	if !ok || task.Status != state.TaskOpen || task.PRDraft {
		return state.Task{}, false
	}
	return task, true
}

func (s *Server) defaultBaseBranchForTask(taskID string) string {
	if run, ok := s.Store.LastRunForTask(taskID); ok && run.BaseBranch != "" {
		return run.BaseBranch
	}
	return "main"
}

func (s *Server) firstKnownBaseBranch(taskID, preferred string) string {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		return preferred
	}
	return s.defaultBaseBranchForTask(taskID)
}

func (s *Server) defaultHeadBranchForTask(taskID string) string {
	if run, ok := s.Store.LastRunForTask(taskID); ok && run.HeadBranch != "" {
		return run.HeadBranch
	}
	return ""
}

func (s *Server) firstKnownHeadBranch(taskID, preferred string) string {
	if preferred = strings.TrimSpace(preferred); preferred != "" {
		return preferred
	}
	return s.defaultHeadBranchForTask(taskID)
}

func (s *Server) firstKnownInstruction(taskID, fallback string) string {
	if run, ok := s.Store.LastRunForTask(taskID); ok && strings.TrimSpace(run.Instruction) != "" {
		return strings.TrimSpace(run.Instruction)
	}
	return strings.TrimSpace(fallback)
}

func (s *Server) resolvePRBranches(ctx context.Context, repo string, prNumber int, baseBranch, headBranch string) (string, string) {
	baseBranch = strings.TrimSpace(baseBranch)
	headBranch = strings.TrimSpace(headBranch)
	if baseBranch != "" && headBranch != "" {
		return baseBranch, headBranch
	}
	if s.GitHub == nil || strings.TrimSpace(repo) == "" || prNumber <= 0 {
		return baseBranch, headBranch
	}

	pr, err := s.GitHub.GetPullRequest(ctx, repo, prNumber)
	if err != nil {
		log.Printf("resolve PR branches repo=%s pr=%d failed: %v", repo, prNumber, err)
		return baseBranch, headBranch
	}
	if baseBranch == "" {
		baseBranch = strings.TrimSpace(pr.Base.Ref)
	}
	if headBranch == "" {
		headBranch = strings.TrimSpace(pr.Head.Ref)
	}
	return baseBranch, headBranch
}

func (s *Server) cancelQueuedReviewThreadRuns(taskID, repo string, prNumber int, reviewThreadID int64, reason string) {
	taskID = strings.TrimSpace(taskID)
	repo = strings.TrimSpace(repo)
	reason = strings.TrimSpace(reason)
	if taskID == "" || repo == "" || prNumber <= 0 || reviewThreadID <= 0 {
		return
	}
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRuns(10000) {
		if run.TaskID != taskID || run.Repo != repo || run.PRNumber != prNumber || run.Trigger != "pr_review_thread" || run.Status != state.StatusQueued {
			continue
		}
		target, ok, err := loadPersistedRunResponseTarget(s.Store, run)
		if err != nil {
			log.Printf("run %s load response target for review thread cancellation failed: %v", run.ID, err)
			continue
		}
		if !ok || target.ReviewThreadID != reviewThreadID {
			continue
		}
		if _, err := s.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(state.RunStatusReasonReviewThreadResolved)); err != nil {
			log.Printf("run %s cancel for resolved review thread failed: %v", run.ID, err)
		}
	}
}
