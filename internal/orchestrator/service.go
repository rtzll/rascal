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
const runCompletionCommentMarkerFile = "completion_comment_posted.json"
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

type NotificationSink interface {
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

type runStateStore interface {
	SetRunStatusWithReason(runID string, status state.RunStatus, errText string, statusReason state.RunStatusReason) (state.Run, error)
	UpdateRun(id string, fn func(*state.Run) error) (state.Run, error)
}

type RunStore interface {
	runStateStore
	ActiveSchedulerPause(scope string, now time.Time) (time.Time, string, bool, error)
	CancelQueuedRuns(taskID, reason string, statusReason state.RunStatusReason) error
	ClaimNextQueuedRun(preferredTaskID string) (state.Run, bool, error)
	ClaimRunStart(runID string) (state.Run, bool, error)
	ClearRunCancel(runID string) error
	ClearSchedulerPause(scope string) error
	CountRunLeasesByOwner(ownerID string) int
	DeleteRunExecution(runID string) error
	DeleteRunLease(runID string) error
	DeleteRunLeaseForOwner(runID, ownerID string) error
	DeleteTaskSession(taskID string) error
	GetActiveCredentialLeaseByRunID(runID string) (state.CredentialLease, bool, error)
	GetRun(id string) (state.Run, bool)
	GetRunCancel(runID string) (state.RunCancelRequest, bool)
	GetRunCredentialInfo(runID string) (state.RunCredentialInfo, bool)
	GetRunExecution(runID string) (state.RunExecution, bool)
	GetRunLease(runID string) (state.RunLease, bool)
	GetRunTokenUsage(runID string) (state.RunTokenUsage, bool)
	GetTaskSession(taskID string) (state.TaskSession, bool)
	IsTaskCompleted(taskID string) bool
	ListRuns(limit int) []state.Run
	ListRunningRuns() []state.Run
	PauseScheduler(scope, reason string, until time.Time) (time.Time, error)
	RequestRunCancel(runID, reason, source string) error
	RenewRunLease(runID, ownerID string, ttl time.Duration) (bool, error)
	SetCredentialStatus(credentialID string, status state.CredentialStatus, until *time.Time, lastError string) error
	SetTaskPR(taskID, repo string, prNumber int) error
	UpsertRunExecution(exec state.RunExecution) (state.RunExecution, error)
	UpsertRunLease(runID, ownerID string, ttl time.Duration) error
	UpsertRunTokenUsage(usage state.RunTokenUsage) (state.RunTokenUsage, error)
	UpsertTaskSession(in state.UpsertTaskSessionInput) (state.TaskSession, error)
	UpdateRunExecutionState(runID string, status state.RunExecutionStatus, exitCode int, lastObservedAt time.Time) (state.RunExecution, error)
}

type RunNotifier interface {
	AddIssueReaction(repo string, issueNumber int, reaction string)
	CleanupAgentSessions()
	NotifyRunStarted(run state.Run, sessionMode rt.SessionMode, sessionResume bool)
	NotifyRunTerminal(run state.Run)
}

type Server struct {
	Config            config.ServerConfig
	Store             *state.Store
	Runner            runner.Runner
	GitHub            GitHubClient
	Notifier          NotificationSink
	Broker            credentials.CredentialBroker
	Cipher            credentials.Cipher
	CredentialManager *credentials.CredentialManager
	SM                *RunStateMachine
	Supervisor        *ExecutionSupervisor
	Scheduler         *RunScheduler

	mu            sync.Mutex
	runCancels    map[string]context.CancelFunc
	MaxConcurrent int
	draining      bool
	InstanceID    string

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
	HeadSHA         string
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
	s := &Server{
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
	s.Supervisor = NewExecutionSupervisor(
		func() config.ServerConfig { return s.Config },
		s.Store,
		s.Runner,
		s.Broker,
		s,
		s.SM,
		s.InstanceID,
	)
	s.Supervisor.Tick = s.supervisorTick
	s.Supervisor.StartRetryBackoff = s.startRetryBackoff
	s.Supervisor.PauseScheduler = func(until time.Time, reason string) time.Time {
		if s.Scheduler == nil {
			if until.IsZero() {
				return time.Now().UTC().Add(defaultUsageLimitPause)
			}
			return until.UTC()
		}
		return s.Scheduler.PauseUntil(until, reason)
	}
	s.Supervisor.OnRunFinished = s.finishRun
	s.Supervisor.BeforeSuperviseHook = func(runID string) {
		if s.BeforeSupervise != nil {
			s.BeforeSupervise(runID)
		}
	}
	s.Supervisor.AfterRunCleanupHook = func(runID string) {
		if s.AfterRunCleanup != nil {
			s.AfterRunCleanup(runID)
		}
	}
	s.Scheduler = NewRunScheduler(s.Store, s.SM, s.Supervisor, s.InstanceID)
	s.Scheduler.ConcurrencyLimit = s.concurrencyLimit
	s.Scheduler.IsDraining = s.isDraining
	return s
}

func (s *Server) credentialManager() *credentials.CredentialManager {
	if s.CredentialManager != nil {
		return s.CredentialManager
	}
	return credentials.NewCredentialManager(s.Store, s.Broker)
}

func (s *Server) notifier() NotificationSink {
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

func (s *Server) releaseDeliveryClaimBestEffort(claim state.DeliveryClaim) {
	if err := s.Store.ReleaseDeliveryClaim(claim); err != nil {
		log.Printf("release delivery claim %s failed: %v", claim.ID, err)
	}
}

func (s *Server) cancelQueuedRunsBestEffort(taskID, reason string, statusReason state.RunStatusReason) {
	if err := s.Store.CancelQueuedRuns(taskID, reason, statusReason); err != nil {
		log.Printf("task %s cancel queued runs failed: %v", taskID, err)
	}
}

func (s *Server) finishRun(run state.Run) {
	if runStatusIsDone(run.Status) {
		s.clearRunCancelBestEffort(run.ID)
		s.cleanupRunSecretsBestEffort(run.ID, run.RunDir)
	}
	taskCompleted := s.Store.IsTaskCompleted(run.TaskID)

	if taskCompleted {
		s.cancelQueuedRunsBestEffort(run.TaskID, "task completed; canceled pending runs", state.RunStatusReasonTaskCompleted)
	}

	if !s.isDraining() {
		s.ScheduleRuns(run.TaskID)
	}
}

func (s *Server) cleanupRunSecretsBestEffort(runID, runDir string) {
	secretsDir := runner.SecretsDir(runDir)
	if strings.TrimSpace(secretsDir) == "" {
		return
	}
	if err := os.RemoveAll(secretsDir); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("run %s remove secrets dir failed: %v", runID, err)
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

func (s *Server) syncComponents() {
	if s.Supervisor != nil {
		s.Supervisor.Store = s.Store
		s.Supervisor.Runner = s.Runner
		s.Supervisor.Broker = s.Broker
		s.Supervisor.Notifier = s
		s.Supervisor.SM = s.SM
		s.Supervisor.InstanceID = s.InstanceID
	}
	if s.Scheduler != nil {
		s.Scheduler.Store = s.Store
		s.Scheduler.SM = s.SM
		s.Scheduler.Supervisor = s.Supervisor
		s.Scheduler.InstanceID = s.InstanceID
	}
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
	s.mu.Unlock()
	if s.Scheduler != nil {
		s.Scheduler.StopResumeTimer()
	}
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

func (s *Server) ActiveSupervisorCount() int {
	if s.Supervisor == nil {
		return 0
	}
	s.Supervisor.mu.Lock()
	defer s.Supervisor.mu.Unlock()
	return len(s.Supervisor.runCancels)
}

func (s *Server) WaitForNoActiveSupervisors(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if s.ActiveSupervisorCount() == 0 {
			return nil
		}
		time.Sleep(10 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for active run supervisors to stop")
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
	return pendingRunCancelStatusFromStore(s.Store, runID)
}

func (s *Server) AddIssueReaction(repo string, issueNumber int, reaction string) {
	s.addIssueReactionBestEffort(repo, issueNumber, reaction)
}

func (s *Server) CleanupAgentSessions() {
	s.cleanupAgentSessionsBestEffort()
}

func (s *Server) NotifyRunStarted(run state.Run, sessionMode rt.SessionMode, sessionResume bool) {
	s.PostRunStartCommentBestEffort(run, sessionMode, sessionResume)
}

func (s *Server) NotifyRunTerminal(run state.Run) {
	s.notifyRunTerminalGitHubBestEffort(run)
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
	case string(state.RunStatusReasonPRSynchronized):
		return state.RunStatusReasonPRSynchronized
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
