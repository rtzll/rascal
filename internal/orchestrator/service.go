package orchestrator

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/runner"
	rt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

var errTaskCompleted = errors.New("task is already completed")
var errServerDraining = errors.New("orchestrator is draining")

var ErrTaskCompleted = errTaskCompleted
var ErrServerDraining = errServerDraining

const runLeaseTTL = 90 * time.Second
const runSupervisorTick = 1 * time.Second
const RunResponseTargetFile = "response_target.json"
const runStartCommentMarkerFile = "start_comment_posted.json"
const runFailureCommentMarkerFile = "failure_comment_posted.json"
const runStartCommentBodyMarker = "<!-- rascal:start-comment -->"
const runCompletionCommentBodyMarker = "<!-- rascal:completion-comment -->"
const agentLogFile = "agent.ndjson"
const legacyAgentLogFile = "goose.ndjson"
const agentOutputFile = "agent_output.txt"
const runFailureCommentBodyMarker = "<!-- rascal:failure-comment -->"
const schedulerPauseScope = "workers"
const defaultUsageLimitPause = 15 * time.Minute
const minimumUsageLimitPause = 1 * time.Minute

const RunLeaseTTL = runLeaseTTL
const RunStartCommentBodyMarker = runStartCommentBodyMarker
const RunCompletionCommentBodyMarker = runCompletionCommentBodyMarker
const SchedulerPauseScope = schedulerPauseScope

var usageLimitPattern = regexp.MustCompile(`(?i)(?:you['’]?ve hit your usage limit|hit your usage limit|usage limit)`)
var retryAtPattern = regexp.MustCompile(`(?i)try again at ([^\r\n.]+)`)
var retryInPattern = regexp.MustCompile(`(?i)try again in ([^\r\n.]+)`)
var ordinalDayPattern = regexp.MustCompile(`\b(\d{1,2})(st|nd|rd|th)\b`)
var durationComponentPattern = regexp.MustCompile(`(?i)(\d+)\s*(d(?:ays?)?|h(?:ours?|rs?)?|m(?:in(?:ute)?s?)?|s(?:ec(?:ond)?s?)?)`)

type GitHubClient interface {
	GetIssue(ctx context.Context, repo string, issueNumber int) (ghapi.IssueData, error)
	GetPullRequest(ctx context.Context, repo string, pullNumber int) (ghapi.PullRequest, error)
	AddIssueReaction(ctx context.Context, repo string, issueNumber int, content string) error
	RemoveIssueReactions(ctx context.Context, repo string, issueNumber int) error
	AddIssueCommentReaction(ctx context.Context, repo string, commentID int64, content string) error
	AddPullRequestReviewReaction(ctx context.Context, repo string, pullNumber int, reviewID int64, content string) error
	AddPullRequestReviewCommentReaction(ctx context.Context, repo string, commentID int64, content string) error
	CreateIssueComment(ctx context.Context, repo string, issueNumber int, body string) error
	ListIssueComments(ctx context.Context, repo string, issueNumber int) ([]ghapi.Comment, error)
}

type RunNotifier interface {
	NotifyRunStarted(run state.Run, sessionMode rt.SessionMode, sessionResume bool)
	NotifyRunCompleted(run state.Run)
	NotifyRunFailed(run state.Run)
	NotifyRunTerminal(run state.Run)
	NotifyInvalidRuntimeLabel(repo string, issueNumber int, err error)
	ReactToIssue(repo string, issueNumber int, reaction string)
	ClearIssueReactions(repo string, issueNumber int)
	ReactToIssueComment(repo string, commentID int64, reaction string)
	ReactToPullRequestReview(repo string, pullNumber int, reviewID int64, reaction string)
	ReactToPullRequestReviewComment(repo string, commentID int64, reaction string)
}

type Server struct {
	Config            config.ServerConfig
	Store             *state.Store
	Runner            runner.Runner
	GitHub            GitHubClient
	Notifier          RunNotifier
	Broker            credentials.CredentialBroker
	Cipher            credentials.Cipher
	CredentialManager *credentials.CredentialManager
	SM                *RunStateMachine

	mu            sync.Mutex
	runCancels    map[string]context.CancelFunc
	scheduleMu    sync.Mutex
	MaxConcurrent int
	draining      bool
	InstanceID    string
	resumeTimer   *time.Timer
	resumeAt      time.Time

	SupervisorInterval time.Duration
	RetryBackoff       func(attempt int) time.Duration
	StopSupervisors    bool
	BeforeSupervise    func(runID string)
	AfterRunCleanup    func(runID string)

	runResultListener net.Listener
	runResultWG       sync.WaitGroup
}

type RunRequest struct {
	TaskID          string
	Repo            string
	Instruction     string
	AgentRuntime    *rt.Runtime // optional per-request override; nil = server default
	BaseBranch      string
	HeadBranch      string
	Trigger         runtrigger.Name
	IssueNumber     int
	PRNumber        int
	PRStatus        state.PRStatus
	Context         string
	Debug           *bool
	CreatedByUserID string

	ResponseTarget *RunResponseTarget
}

type RunResponseTarget struct {
	Repo           string          `json:"repo"`
	IssueNumber    int             `json:"issue_number"`
	RequestedBy    string          `json:"requested_by,omitempty"`
	Trigger        runtrigger.Name `json:"trigger"`
	ReviewThreadID int64           `json:"review_thread_id,omitempty"`
}

type RunFailureSummary struct {
	Headline string
	RetryAt  string
	Reason   string
}

type createTaskRequest = api.CreateTaskRequest
type createIssueTaskRequest = api.CreateIssueTaskRequest
type createCredentialRequest = api.CreateCredentialRequest
type updateCredentialRequest = api.UpdateCredentialRequest

func NewServer(cfg config.ServerConfig, store *state.Store, r runner.Runner, gh GitHubClient, broker credentials.CredentialBroker, cipher credentials.Cipher, instanceID string) *Server {
	if strings.TrimSpace(instanceID) == "" {
		instanceID = fmt.Sprintf("%s-%d-%d", strings.TrimSpace(cfg.Slot), os.Getpid(), time.Now().UTC().UnixNano())
	}
	return &Server{
		Config:        cfg,
		Store:         store,
		Runner:        r,
		GitHub:        gh,
		Broker:        broker,
		Cipher:        cipher,
		SM:            NewRunStateMachine(store),
		runCancels:    make(map[string]context.CancelFunc),
		MaxConcurrent: defaultMaxConcurrent(),
		InstanceID:    instanceID,
	}
}

func (s *Server) credentialManager() *credentials.CredentialManager {
	if s.CredentialManager != nil {
		return s.CredentialManager
	}
	return credentials.NewCredentialManager(s.Store, s.Broker)
}

func (s *Server) notifier() RunNotifier {
	if s.Notifier != nil {
		return s.Notifier
	}
	return NewGitHubRunNotifier(s.Config, s.Store, s.GitHub)
}

func (s *Server) RecoverQueuedCancels() {
	runs := s.Store.ListRuns(10000)
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.Status != state.StatusQueued {
			continue
		}
		if reason, statusReason, ok := s.pendingRunCancelStatus(run.ID); ok {
			if _, err := s.SM.Transition(run.ID, state.StatusCanceled, WithError(reason), WithReason(statusReason)); err != nil {
				log.Printf("run %s recover queued cancel failed: %v", run.ID, err)
			}
			s.clearRunCancelBestEffort(run.ID)
		}
	}
}

func (s *Server) clearRunCancelBestEffort(runID string) {
	if err := s.Store.ClearRunCancel(runID); err != nil {
		log.Printf("run %s clear cancel request failed: %v", runID, err)
	}
}

func (s *Server) deleteRunLeaseBestEffort(runID string) {
	if err := s.Store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s delete run lease failed: %v", runID, err)
	}
}

func (s *Server) deleteRunExecutionBestEffort(runID string) {
	if err := s.Store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s delete run execution failed: %v", runID, err)
	}
}

func (s *Server) releaseDeliveryClaimBestEffort(claim state.DeliveryClaim) {
	if err := s.Store.ReleaseDeliveryClaim(claim); err != nil {
		log.Printf("release delivery claim %s failed: %v", claim.ID, err)
	}
}

func (s *Server) setTaskPRBestEffort(taskID, repo string, prNumber int) {
	if err := s.Store.SetTaskPR(taskID, repo, prNumber); err != nil {
		log.Printf("task %s set PR #%d failed: %v", taskID, prNumber, err)
	}
}

func (s *Server) cancelQueuedRunsBestEffort(taskID, reason string, statusReason state.RunStatusReason) {
	if err := s.Store.CancelQueuedRuns(taskID, reason, statusReason); err != nil {
		log.Printf("task %s cancel queued runs failed: %v", taskID, err)
	}
}

func (s *Server) requestRunCancelBestEffort(runID, reason, source string) {
	if err := s.Store.RequestRunCancel(runID, reason, source); err != nil {
		log.Printf("run %s request cancel failed: %v", runID, err)
	}
}

func (s *Server) removeRunExecutionBestEffort(ctx context.Context, handle runner.ExecutionHandle, runID, note string) {
	err := s.Runner.Remove(ctx, handle)
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove execution failed (%s): %v", runID, note, err)
	}
}

func (s *Server) finishRun(run state.Run) {
	if runStatusIsDone(run.Status) {
		s.clearRunCancelBestEffort(run.ID)
	}
	taskCompleted := s.Store.IsTaskCompleted(run.TaskID)

	if taskCompleted {
		s.cancelQueuedRunsBestEffort(run.TaskID, "task completed; canceled pending runs", state.RunStatusReasonTaskCompleted)
	}

	if !s.isDraining() {
		s.ScheduleRuns(run.TaskID)
	}
}

func defaultMaxConcurrent() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

func (s *Server) supervisorTick() time.Duration {
	if s != nil && s.SupervisorInterval > 0 {
		return s.SupervisorInterval
	}
	return runSupervisorTick
}

// transitionOrGetRun attempts a status transition via the state machine.
// On failure, it falls back to the persisted run state (or the provided
// fallback) so callers always have a run to pass to notification and
// cleanup functions.
func (s *Server) transitionOrGetRun(fallback state.Run, to state.RunStatus, opts ...TransitionOption) state.Run {
	updated, err := s.SM.Transition(fallback.ID, to, opts...)
	if err != nil {
		log.Printf("run %s transition to %q failed: %v", fallback.ID, to, err)
		if got, ok := s.Store.GetRun(fallback.ID); ok {
			return got
		}
		return fallback
	}
	return updated
}

func (s *Server) startRetryBackoff(attempt int) time.Duration {
	if s != nil && s.RetryBackoff != nil {
		if backoff := s.RetryBackoff(attempt); backoff > 0 {
			return backoff
		}
		return time.Millisecond
	}
	if attempt < 1 {
		attempt = 1
	}
	return time.Duration(attempt) * time.Second
}

func (s *Server) concurrencyLimit() int {
	if s.MaxConcurrent > 0 {
		return s.MaxConcurrent
	}
	return 1
}

func (s *Server) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func (s *Server) BeginDrain() {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return
	}
	s.draining = true
	if s.resumeTimer != nil {
		s.resumeTimer.Stop()
		s.resumeTimer = nil
		s.resumeAt = time.Time{}
	}
	s.mu.Unlock()
}

func (s *Server) WaitForNoActiveRuns(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		active := s.ActiveRunCount()
		if active == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for active runs to finish")
}

func (s *Server) ActiveRunCount() int {
	return s.Store.CountRunLeasesByOwner(s.InstanceID)
}

func (s *Server) isActiveWebhookSlot() bool {
	slot := strings.TrimSpace(s.Config.Slot)
	if slot == "" {
		return true
	}
	activePath := strings.TrimSpace(s.Config.ActiveSlotPath)
	if activePath == "" {
		return true
	}
	data, err := os.ReadFile(activePath)
	if err != nil {
		log.Printf("webhook slot gate: failed reading %s: %v", activePath, err)
		return false
	}
	active := strings.TrimSpace(string(data))
	switch active {
	case "blue", "green":
		return slot == active
	default:
		log.Printf("webhook slot gate: invalid active slot %q from %s", active, activePath)
		return false
	}
}

func (s *Server) pendingRunCancelStatus(runID string) (string, state.RunStatusReason, bool) {
	req, ok := s.Store.GetRunCancel(runID)
	if !ok {
		return "", state.RunStatusReasonNone, false
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "canceled"
	}
	return reason, statusReasonFromCancelSource(req.Source), true
}

func statusReasonFromCancelSource(source string) state.RunStatusReason {
	switch strings.ToLower(strings.TrimSpace(source)) {
	case "user":
		return state.RunStatusReasonUserCanceled
	case string(state.RunStatusReasonIssueClosed), "issue":
		return state.RunStatusReasonIssueClosed
	case string(state.RunStatusReasonIssueEdited):
		return state.RunStatusReasonIssueEdited
	case string(state.RunStatusReasonPRClosed):
		return state.RunStatusReasonPRClosed
	case string(state.RunStatusReasonPRDraft):
		return state.RunStatusReasonPRDraft
	case string(state.RunStatusReasonPRMerged):
		return state.RunStatusReasonPRMerged
	case string(state.RunStatusReasonReviewThreadResolved):
		return state.RunStatusReasonReviewThreadResolved
	case "shutdown":
		return state.RunStatusReasonShutdown
	case string(state.RunStatusReasonDeployReclaimed):
		return state.RunStatusReasonDeployReclaimed
	case "broker":
		return state.RunStatusReasonCredentialLeaseLost
	default:
		return state.RunStatusReasonNone
	}
}

func isCommentTriggeredRun(trigger runtrigger.Name) bool {
	return runtrigger.Normalize(trigger.String()).IsComment()
}
