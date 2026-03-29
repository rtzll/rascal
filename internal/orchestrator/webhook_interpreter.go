package orchestrator

import (
	"encoding/json"
	"fmt"
	"strings"

	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

type WebhookActionKind string

const (
	WebhookActionCreateIssueRun           WebhookActionKind = "create_issue_run"
	WebhookActionCancelTaskRuns           WebhookActionKind = "cancel_task_runs"
	WebhookActionClearIssueReactions      WebhookActionKind = "clear_issue_reactions"
	WebhookActionCompleteIssueTask        WebhookActionKind = "complete_issue_task"
	WebhookActionReopenIssueTask          WebhookActionKind = "reopen_issue_task"
	WebhookActionCreatePRCommentRun       WebhookActionKind = "create_pr_comment_run"
	WebhookActionCreatePRReviewRun        WebhookActionKind = "create_pr_review_run"
	WebhookActionCreatePRReviewCommentRun WebhookActionKind = "create_pr_review_comment_run"
	WebhookActionCreatePRThreadRun        WebhookActionKind = "create_pr_review_thread_run"
	WebhookActionCancelPRThreadRuns       WebhookActionKind = "cancel_pr_review_thread_runs"
	WebhookActionClosePullRequest         WebhookActionKind = "close_pull_request"
	WebhookActionReopenPullRequest        WebhookActionKind = "reopen_pull_request"
	WebhookActionSynchronizePullRequest   WebhookActionKind = "synchronize_pull_request"
	WebhookActionConvertPullRequestDraft  WebhookActionKind = "convert_pull_request_draft"
	WebhookActionReadyPullRequest         WebhookActionKind = "ready_pull_request"
	WebhookActionCreatePRCheckFailureRun  WebhookActionKind = "create_pr_check_failure_run"
)

type WebhookAction struct {
	Kind WebhookActionKind

	TaskID      string
	Repo        string
	IssueNumber int
	PRNumber    int

	Instruction string
	Context     string
	Trigger     runtrigger.Name
	RequestedBy string

	Label  string
	Labels []ghapi.Label

	CommentID      int64
	ReviewID       int64
	ReviewThreadID int64

	BaseBranch string
	HeadBranch string
	HeadSHA    string

	Merged bool

	CancelReason string
	StatusReason state.RunStatusReason
}

type WebhookInterpreter struct {
	botLogin string
}

func NewWebhookInterpreter(botLogin string) WebhookInterpreter {
	return WebhookInterpreter{botLogin: botLogin}
}

func (wi WebhookInterpreter) Interpret(eventType string, payload []byte) ([]WebhookAction, error) {
	switch eventType {
	case "issues":
		return wi.interpretIssues(payload)
	case "issue_comment":
		return wi.interpretIssueComment(payload)
	case "pull_request_review":
		return wi.interpretPullRequestReview(payload)
	case "pull_request_review_comment":
		return wi.interpretPullRequestReviewComment(payload)
	case "pull_request_review_thread":
		return wi.interpretPullRequestReviewThread(payload)
	case "pull_request":
		return wi.interpretPullRequest(payload)
	case "check_run":
		return wi.interpretCheckRun(payload)
	case "check_suite":
		return wi.interpretCheckSuite(payload)
	default:
		return nil, nil
	}
}

func (wi WebhookInterpreter) interpretIssues(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.IssuesEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode issues event: %w", err)
	}
	if ev.Issue.PullRequest != nil || wi.isBotActor(ev.Sender.Login) {
		return nil, nil
	}

	taskID := repoIssueTaskID(ev.Repository.FullName, ev.Issue.Number)
	createIssueRun := WebhookAction{
		Kind:        WebhookActionCreateIssueRun,
		TaskID:      taskID,
		Repo:        ev.Repository.FullName,
		IssueNumber: ev.Issue.Number,
		Instruction: ghapi.IssueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
		RequestedBy: strings.TrimSpace(ev.Sender.Login),
		Labels:      ev.Issue.Labels,
	}

	switch ev.Action {
	case "labeled":
		if !runtime.IsRascalLabel(ev.Label.Name) {
			return nil, nil
		}
		createIssueRun.Label = ev.Label.Name
		createIssueRun.Trigger = runtrigger.NameIssueLabel
		createIssueRun.Context = fmt.Sprintf("Triggered by label '%s' on issue #%d", ev.Label.Name, ev.Issue.Number)
		return []WebhookAction{createIssueRun}, nil
	case "unlabeled":
		if !runtime.IsRascalLabel(ev.Label.Name) {
			return nil, nil
		}
		return []WebhookAction{
			{
				Kind:         WebhookActionCancelTaskRuns,
				TaskID:       taskID,
				CancelReason: "label removed",
				StatusReason: state.RunStatusReasonUserCanceled,
			},
			{
				Kind:        WebhookActionClearIssueReactions,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			},
		}, nil
	case "edited":
		if !ghapi.IssueHasRascalLabel(ev.Issue.Labels) {
			return nil, nil
		}
		createIssueRun.Trigger = runtrigger.NameIssueEdited
		createIssueRun.Context = fmt.Sprintf("Triggered by issue edit on issue #%d", ev.Issue.Number)
		return []WebhookAction{
			{
				Kind:         WebhookActionCancelTaskRuns,
				TaskID:       taskID,
				CancelReason: "issue edited",
				StatusReason: state.RunStatusReasonIssueEdited,
			},
			createIssueRun,
		}, nil
	case "closed":
		if !ghapi.IssueHasRascalLabel(ev.Issue.Labels) {
			return nil, nil
		}
		return []WebhookAction{{
			Kind:        WebhookActionCompleteIssueTask,
			TaskID:      taskID,
			Repo:        ev.Repository.FullName,
			IssueNumber: ev.Issue.Number,
		}}, nil
	case "reopened":
		if !ghapi.IssueHasRascalLabel(ev.Issue.Labels) {
			return nil, nil
		}
		createIssueRun.Trigger = runtrigger.NameIssueReopened
		createIssueRun.Context = fmt.Sprintf("Triggered by issue reopen on issue #%d", ev.Issue.Number)
		return []WebhookAction{
			{
				Kind:        WebhookActionReopenIssueTask,
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			},
			createIssueRun,
		}, nil
	default:
		return nil, nil
	}
}

func (wi WebhookInterpreter) interpretIssueComment(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.IssueCommentEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode issue_comment event: %w", err)
	}
	if ev.Issue.PullRequest == nil || !isOpenGitHubState(ev.Issue.State) {
		return nil, nil
	}
	switch ev.Action {
	case "created":
	case "edited":
		if !ghapi.IssueCommentBodyChanged(ev) {
			return nil, nil
		}
	default:
		return nil, nil
	}
	if ghapi.IsAutomationComment(ev.Comment.Body, runCompletionCommentBodyMarker, runStartCommentBodyMarker, runFailureCommentBodyMarker) {
		return nil, nil
	}
	if wi.isBotActor(ev.Comment.User.Login) || wi.isBotActor(ev.Sender.Login) {
		return nil, nil
	}
	return []WebhookAction{{
		Kind:        WebhookActionCreatePRCommentRun,
		Repo:        ev.Repository.FullName,
		PRNumber:    ev.Issue.Number,
		Instruction: fmt.Sprintf("Address PR #%d feedback", ev.Issue.Number),
		Context:     strings.TrimSpace(ev.Comment.Body),
		Trigger:     runtrigger.NamePRComment,
		RequestedBy: strings.TrimSpace(ev.Sender.Login),
		CommentID:   ev.Comment.ID,
	}}, nil
}

func (wi WebhookInterpreter) interpretPullRequestReview(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.PullRequestReviewEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode pull_request_review event: %w", err)
	}
	if ev.Action != "submitted" || !isOpenGitHubState(ev.PullRequest.State) {
		return nil, nil
	}
	if wi.isBotActor(ev.Review.User.Login) || wi.isBotActor(ev.Sender.Login) {
		return nil, nil
	}
	contextText := strings.TrimSpace(ev.Review.Body)
	if contextText == "" {
		contextText = fmt.Sprintf("review state: %s", ev.Review.State)
	}
	return []WebhookAction{{
		Kind:        WebhookActionCreatePRReviewRun,
		Repo:        ev.Repository.FullName,
		PRNumber:    ev.PullRequest.Number,
		Instruction: fmt.Sprintf("Address PR #%d review feedback", ev.PullRequest.Number),
		Context:     contextText,
		Trigger:     runtrigger.NamePRReview,
		RequestedBy: strings.TrimSpace(ev.Sender.Login),
		ReviewID:    ev.Review.ID,
		BaseBranch:  ev.PullRequest.Base.Ref,
		HeadBranch:  ev.PullRequest.Head.Ref,
	}}, nil
}

func (wi WebhookInterpreter) interpretPullRequestReviewComment(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.PullRequestReviewCommentEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode pull_request_review_comment event: %w", err)
	}
	if !isOpenGitHubState(ev.PullRequest.State) {
		return nil, nil
	}
	switch ev.Action {
	case "created":
	case "edited":
		if !ghapi.ReviewCommentBodyChanged(ev) {
			return nil, nil
		}
	default:
		return nil, nil
	}
	if wi.isBotActor(ev.Comment.User.Login) || wi.isBotActor(ev.Sender.Login) {
		return nil, nil
	}
	contextText := strings.TrimSpace(ev.Comment.Body)
	if location := ghapi.FormatReviewCommentLocation(ev.Comment.Path, ev.Comment.StartLine, ev.Comment.Line); location != "" {
		if contextText == "" {
			contextText = fmt.Sprintf("inline review comment at %s", location)
		} else {
			contextText = fmt.Sprintf("%s\n\nInline comment location: %s", contextText, location)
		}
	}
	return []WebhookAction{{
		Kind:        WebhookActionCreatePRReviewCommentRun,
		Repo:        ev.Repository.FullName,
		PRNumber:    ev.PullRequest.Number,
		Instruction: fmt.Sprintf("Address PR #%d inline review comment", ev.PullRequest.Number),
		Context:     contextText,
		Trigger:     runtrigger.NamePRReviewComment,
		RequestedBy: strings.TrimSpace(ev.Sender.Login),
		CommentID:   ev.Comment.ID,
		BaseBranch:  ev.PullRequest.Base.Ref,
		HeadBranch:  ev.PullRequest.Head.Ref,
	}}, nil
}

func (wi WebhookInterpreter) interpretPullRequestReviewThread(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.PullRequestReviewThreadEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode pull_request_review_thread event: %w", err)
	}
	if !isOpenGitHubState(ev.PullRequest.State) {
		return nil, nil
	}
	switch ev.Action {
	case "unresolved":
		if wi.isBotActor(ev.Sender.Login) {
			return nil, nil
		}
		return []WebhookAction{{
			Kind:           WebhookActionCreatePRThreadRun,
			Repo:           ev.Repository.FullName,
			PRNumber:       ev.PullRequest.Number,
			Instruction:    fmt.Sprintf("Address PR #%d unresolved review thread", ev.PullRequest.Number),
			Context:        ghapi.ReviewThreadContext(ev.Thread),
			Trigger:        runtrigger.NamePRReviewThread,
			RequestedBy:    strings.TrimSpace(ev.Sender.Login),
			ReviewThreadID: ev.Thread.ID,
			BaseBranch:     ev.PullRequest.Base.Ref,
			HeadBranch:     ev.PullRequest.Head.Ref,
		}}, nil
	case "resolved":
		return []WebhookAction{{
			Kind:           WebhookActionCancelPRThreadRuns,
			Repo:           ev.Repository.FullName,
			PRNumber:       ev.PullRequest.Number,
			ReviewThreadID: ev.Thread.ID,
			CancelReason:   "review thread resolved",
		}}, nil
	default:
		return nil, nil
	}
}

func (wi WebhookInterpreter) interpretPullRequest(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.PullRequestEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode pull_request event: %w", err)
	}
	switch ev.Action {
	case "closed":
		return []WebhookAction{{
			Kind:     WebhookActionClosePullRequest,
			Repo:     ev.Repository.FullName,
			PRNumber: ev.PullRequest.Number,
			Merged:   ev.PullRequest.Merged,
		}}, nil
	case "reopened":
		return []WebhookAction{{
			Kind:     WebhookActionReopenPullRequest,
			Repo:     ev.Repository.FullName,
			PRNumber: ev.PullRequest.Number,
		}}, nil
	case "synchronize":
		if wi.isBotActor(ev.Sender.Login) || !isOpenGitHubState(ev.PullRequest.State) {
			return nil, nil
		}
		return []WebhookAction{{
			Kind:         WebhookActionSynchronizePullRequest,
			Repo:         ev.Repository.FullName,
			PRNumber:     ev.PullRequest.Number,
			Trigger:      runtrigger.NamePRSynchronize,
			RequestedBy:  strings.TrimSpace(ev.Sender.Login),
			BaseBranch:   strings.TrimSpace(ev.PullRequest.Base.Ref),
			HeadBranch:   strings.TrimSpace(ev.PullRequest.Head.Ref),
			HeadSHA:      strings.TrimSpace(ev.PullRequest.Head.SHA),
			CancelReason: "pull request synchronized",
			StatusReason: state.RunStatusReasonPRSynchronized,
		}}, nil
	case "converted_to_draft":
		return []WebhookAction{{
			Kind:     WebhookActionConvertPullRequestDraft,
			Repo:     ev.Repository.FullName,
			PRNumber: ev.PullRequest.Number,
		}}, nil
	case "ready_for_review":
		return []WebhookAction{{
			Kind:     WebhookActionReadyPullRequest,
			Repo:     ev.Repository.FullName,
			PRNumber: ev.PullRequest.Number,
		}}, nil
	default:
		return nil, nil
	}
}

func (wi WebhookInterpreter) interpretCheckRun(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.CheckRunEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode check_run event: %w", err)
	}
	if ev.Action != "completed" || !isFailedCheckConclusion(ev.CheckRun.Conclusion) {
		return nil, nil
	}
	return wi.buildCheckFailureActions(
		ev.Repository.FullName,
		ev.CheckRun.PullRequests,
		ev.CheckRun.CheckSuite.HeadBranch,
		ev.CheckRun.HeadSHA,
		ev.CheckRun.Name,
		renderCheckFailureContext(ev.CheckRun.Name, ev.CheckRun.Conclusion, ev.CheckRun.DetailsURL, ev.CheckRun.Output),
	), nil
}

func (wi WebhookInterpreter) interpretCheckSuite(payload []byte) ([]WebhookAction, error) {
	var ev ghapi.CheckSuiteEvent
	if err := json.Unmarshal(payload, &ev); err != nil {
		return nil, fmt.Errorf("decode check_suite event: %w", err)
	}
	if ev.Action != "completed" || !isFailedCheckConclusion(ev.CheckSuite.Conclusion) {
		return nil, nil
	}
	return wi.buildCheckFailureActions(
		ev.Repository.FullName,
		ev.CheckSuite.PullRequests,
		ev.CheckSuite.HeadBranch,
		ev.CheckSuite.HeadSHA,
		"check suite",
		renderCheckFailureContext("check suite", ev.CheckSuite.Conclusion, "", ghapi.CheckOutput{}),
	), nil
}

func (wi WebhookInterpreter) buildCheckFailureActions(repo string, prs []ghapi.CheckPullRequest, headBranch, headSHA, checkName, contextText string) []WebhookAction {
	repo = state.NormalizeRepo(repo)
	headBranch = strings.TrimSpace(headBranch)
	headSHA = strings.TrimSpace(headSHA)
	if repo == "" {
		return nil
	}
	action := WebhookAction{
		Kind:        WebhookActionCreatePRCheckFailureRun,
		Repo:        repo,
		Trigger:     runtrigger.NamePRCheckFailure,
		Context:     contextText,
		Instruction: "Investigate and fix CI failure on Rascal branch",
		HeadBranch:  headBranch,
		HeadSHA:     headSHA,
	}
	if len(prs) > 0 {
		action.PRNumber = prs[0].Number
		action.BaseBranch = strings.TrimSpace(prs[0].Base.Ref)
		if action.HeadBranch == "" {
			action.HeadBranch = strings.TrimSpace(prs[0].Head.Ref)
		}
		action.Instruction = fmt.Sprintf("Investigate and fix CI failure on PR #%d", action.PRNumber)
		return []WebhookAction{action}
	}
	if !isRascalBranch(action.HeadBranch) {
		return nil
	}
	if strings.TrimSpace(checkName) != "" {
		action.Instruction = fmt.Sprintf("Investigate and fix CI failure for check '%s'", strings.TrimSpace(checkName))
	}
	return []WebhookAction{action}
}

func isFailedCheckConclusion(conclusion string) bool {
	switch strings.ToLower(strings.TrimSpace(conclusion)) {
	case "failure", "timed_out", "action_required", "startup_failure":
		return true
	default:
		return false
	}
}

func renderCheckFailureContext(name, conclusion, detailsURL string, output ghapi.CheckOutput) string {
	lines := []string{
		fmt.Sprintf("CI check failure: %s", firstNonEmpty(strings.TrimSpace(name), "unknown check")),
		fmt.Sprintf("Conclusion: %s", firstNonEmpty(strings.TrimSpace(conclusion), "unknown")),
	}
	if strings.TrimSpace(detailsURL) != "" {
		lines = append(lines, fmt.Sprintf("Details: %s", strings.TrimSpace(detailsURL)))
	}
	if strings.TrimSpace(output.Title) != "" {
		lines = append(lines, fmt.Sprintf("Title: %s", strings.TrimSpace(output.Title)))
	}
	if strings.TrimSpace(output.Summary) != "" {
		lines = append(lines, fmt.Sprintf("Summary: %s", strings.TrimSpace(output.Summary)))
	}
	if strings.TrimSpace(output.Text) != "" {
		lines = append(lines, fmt.Sprintf("Text: %s", strings.TrimSpace(output.Text)))
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

func isRascalBranch(headBranch string) bool {
	return strings.HasPrefix(strings.ToLower(strings.TrimSpace(headBranch)), "rascal/")
}

func (wi WebhookInterpreter) isBotActor(login string) bool {
	login = strings.TrimSpace(strings.ToLower(login))
	if login == "" {
		return false
	}
	if strings.TrimSpace(wi.botLogin) != "" && login == strings.ToLower(strings.TrimSpace(wi.botLogin)) {
		return true
	}
	return strings.Contains(login, "[bot]")
}
