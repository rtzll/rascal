package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"path/filepath"
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
const workerPauseScope = "workers"
const defaultUsageLimitPause = 15 * time.Minute
const minimumUsageLimitPause = 1 * time.Minute

const RunLeaseTTL = runLeaseTTL
const RunStartCommentBodyMarker = runStartCommentBodyMarker
const RunCompletionCommentBodyMarker = runCompletionCommentBodyMarker
const WorkerPauseScope = workerPauseScope

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
}

type Server struct {
	Config   config.ServerConfig
	Store    *state.Store
	Launcher runner.Launcher
	GitHub   GitHubClient
	Broker   credentials.CredentialBroker
	Cipher   credentials.Cipher

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
}

type RunRequest struct {
	TaskID          string
	Repo            string
	Instruction     string
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

type RunCommentMarker struct {
	RunID       string `json:"run_id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	PostedAt    string `json:"posted_at"`
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

func NewServer(cfg config.ServerConfig, store *state.Store, launcher runner.Launcher, gh GitHubClient, broker credentials.CredentialBroker, cipher credentials.Cipher, instanceID string) *Server {
	if strings.TrimSpace(instanceID) == "" {
		instanceID = fmt.Sprintf("%s-%d-%d", strings.TrimSpace(cfg.Slot), os.Getpid(), time.Now().UTC().UnixNano())
	}
	return &Server{
		Config:        cfg,
		Store:         store,
		Launcher:      launcher,
		GitHub:        gh,
		Broker:        broker,
		Cipher:        cipher,
		runCancels:    make(map[string]context.CancelFunc),
		MaxConcurrent: defaultMaxConcurrent(),
		InstanceID:    instanceID,
	}
}

func (s *Server) RecoverQueuedCancels() {
	runs := s.Store.ListRuns(10000)
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		if run.Status != state.StatusQueued {
			continue
		}
		if reason, ok := s.pendingRunCancelReason(run.ID); ok {
			s.setRunStatusBestEffort(run.ID, state.StatusCanceled, reason)
			s.clearRunCancelBestEffort(run.ID)
		}
	}
}

func (s *Server) CreateAndQueueRun(req RunRequest) (state.Run, error) {
	if s.isDraining() {
		return state.Run{}, errServerDraining
	}
	req.Repo = state.NormalizeRepo(req.Repo)
	req.Instruction = strings.TrimSpace(req.Instruction)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	req.HeadBranch = strings.TrimSpace(req.HeadBranch)
	req.Context = strings.TrimSpace(req.Context)
	req.CreatedByUserID = strings.TrimSpace(req.CreatedByUserID)
	if req.Repo == "" || req.Instruction == "" {
		return state.Run{}, fmt.Errorf("repo and task are required")
	}
	if req.CreatedByUserID == "" {
		req.CreatedByUserID = "system"
	}
	if req.Trigger == "" {
		req.Trigger = runtrigger.NameCLI
	} else {
		req.Trigger = runtrigger.Normalize(req.Trigger.String())
		if !req.Trigger.IsKnown() {
			return state.Run{}, fmt.Errorf("unknown workflow trigger %q", req.Trigger)
		}
	}
	if req.PRStatus == "" {
		if req.PRNumber > 0 {
			req.PRStatus = state.PRStatusOpen
		} else {
			req.PRStatus = state.PRStatusNone
		}
	}
	debugEnabled := true
	if req.Debug != nil {
		debugEnabled = *req.Debug
	}

	runID, err := state.NewRunID()
	if err != nil {
		return state.Run{}, fmt.Errorf("create run ID: %w", err)
	}
	if req.TaskID == "" {
		req.TaskID = runID
	}
	if s.Store.IsTaskCompleted(req.TaskID) {
		return state.Run{}, errTaskCompleted
	}
	if existingTask, ok := s.Store.GetTask(req.TaskID); ok && existingTask.AgentRuntime != s.Config.AgentRuntime {
		if err := s.Store.DeleteTaskAgentSession(req.TaskID); err != nil {
			return state.Run{}, fmt.Errorf("clear stale task session for backend migration: %w", err)
		}
	}

	lastRun, hasLastRun := s.Store.LastRunForTask(req.TaskID)
	if req.BaseBranch == "" {
		if hasLastRun && lastRun.BaseBranch != "" {
			req.BaseBranch = lastRun.BaseBranch
		} else {
			req.BaseBranch = "main"
		}
	}
	if req.HeadBranch == "" {
		if hasLastRun && (req.Trigger == runtrigger.NamePRComment || req.Trigger == runtrigger.NamePRReview) && lastRun.HeadBranch != "" {
			req.HeadBranch = lastRun.HeadBranch
		} else {
			req.HeadBranch = BuildHeadBranch(req.TaskID, req.Instruction, runID)
		}
	}

	runDir := filepath.Join(s.Config.DataDir, "runs", runID)

	_, err = s.Store.UpsertTask(state.UpsertTaskInput{
		ID:           req.TaskID,
		Repo:         req.Repo,
		AgentRuntime: s.Config.AgentRuntime,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("upsert task: %w", err)
	}
	if err := s.Store.SetTaskCreatedByUser(req.TaskID, req.CreatedByUserID); err != nil {
		return state.Run{}, fmt.Errorf("set task requester: %w", err)
	}

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           runID,
		TaskID:       req.TaskID,
		Repo:         req.Repo,
		Instruction:  req.Instruction,
		AgentRuntime: s.Config.AgentRuntime,
		BaseBranch:   req.BaseBranch,
		HeadBranch:   req.HeadBranch,
		Trigger:      req.Trigger,
		RunDir:       runDir,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
		PRStatus:     req.PRStatus,
		Context:      req.Context,
		Debug:        boolPtr(debugEnabled),
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("persist run: %w", err)
	}
	if err := s.Store.SetRunCreatedByUser(run.ID, req.CreatedByUserID); err != nil {
		return state.Run{}, fmt.Errorf("set run requester: %w", err)
	}

	if err := s.WriteRunFiles(run); err != nil {
		s.setRunStatusBestEffort(run.ID, state.StatusFailed, err.Error())
		return state.Run{}, fmt.Errorf("prepare run files: %w", err)
	}
	if err := s.WriteRunResponseTarget(run, req.ResponseTarget); err != nil {
		s.setRunStatusBestEffort(run.ID, state.StatusFailed, err.Error())
		return state.Run{}, fmt.Errorf("prepare run response target: %w", err)
	}
	s.ScheduleRuns(run.TaskID)
	return run, nil
}

func (s *Server) WriteRunFiles(run state.Run) (err error) {
	if err := os.MkdirAll(filepath.Join(run.RunDir, "codex"), 0o755); err != nil {
		return fmt.Errorf("create codex run directory: %w", err)
	}

	ctxPayload := RunContextFile{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Instruction: run.Instruction,
		Trigger:     run.Trigger.String(),
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	}
	ctxData, err := json.MarshalIndent(ctxPayload, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal run context: %w", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "context.json"), ctxData, 0o644); err != nil {
		return fmt.Errorf("write run context file: %w", err)
	}

	instructions := InstructionText(run)
	if err := os.WriteFile(filepath.Join(run.RunDir, "instructions.md"), []byte(instructions), 0o644); err != nil {
		return fmt.Errorf("write run instructions: %w", err)
	}

	logLine := fmt.Sprintf("[%s] queued run=%s task=%s trigger=%s\n", time.Now().UTC().Format(time.RFC3339), run.ID, run.TaskID, run.Trigger)
	f, err := os.OpenFile(filepath.Join(run.RunDir, "runner.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return fmt.Errorf("open runner log: %w", err)
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = fmt.Errorf("close runner log: %w", closeErr)
		}
	}()
	_, err = f.WriteString(logLine)
	if err != nil {
		return fmt.Errorf("write runner log entry: %w", err)
	}
	return nil
}

type RunContextFile struct {
	RunID       string `json:"run_id"`
	TaskID      string `json:"task_id"`
	Repo        string `json:"repo"`
	Instruction string `json:"instruction"`
	Trigger     string `json:"trigger"`
	IssueNumber int    `json:"issue_number"`
	PRNumber    int    `json:"pr_number"`
	Context     string `json:"context"`
	Debug       bool   `json:"debug"`
}

func (s *Server) WriteRunResponseTarget(run state.Run, target *RunResponseTarget) error {
	if target == nil {
		return nil
	}
	out := RunResponseTarget{
		Repo:           strings.TrimSpace(target.Repo),
		IssueNumber:    target.IssueNumber,
		RequestedBy:    strings.TrimSpace(target.RequestedBy),
		Trigger:        runtrigger.Normalize(target.Trigger.String()),
		ReviewThreadID: target.ReviewThreadID,
	}
	if out.Repo == "" {
		out.Repo = strings.TrimSpace(run.Repo)
	}
	if out.IssueNumber <= 0 {
		out.IssueNumber = run.PRNumber
	}
	if out.Trigger == "" {
		out.Trigger = runtrigger.Normalize(run.Trigger.String())
	}
	if out.Repo == "" || out.IssueNumber <= 0 {
		return nil
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run response target: %w", err)
	}
	path := filepath.Join(run.RunDir, RunResponseTargetFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write run response target: %w", err)
	}
	return nil
}

func (s *Server) setRunStatusWithFallback(run state.Run, status state.RunStatus, errText string) state.Run {
	updated, err := s.Store.SetRunStatus(run.ID, status, errText)
	if err == nil {
		return updated
	}

	log.Printf("failed to set run status %q for %s: %v", status, run.ID, err)
	now := time.Now().UTC()
	run.Status = status
	run.Error = errText
	run.UpdatedAt = now
	if status == state.StatusRunning {
		run.StartedAt = &now
	}
	if state.IsFinalRunStatus(status) {
		run.CompletedAt = &now
	}
	return run
}

func (s *Server) setRunStatusBestEffort(runID string, status state.RunStatus, errText string) {
	if _, err := s.Store.SetRunStatus(runID, status, errText); err != nil {
		log.Printf("run %s set status %q failed: %v", runID, status, err)
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

func (s *Server) cancelQueuedRunsBestEffort(taskID, reason string) {
	if err := s.Store.CancelQueuedRuns(taskID, reason); err != nil {
		log.Printf("task %s cancel queued runs failed: %v", taskID, err)
	}
}

func (s *Server) updateRunBestEffort(runID string, fn func(*state.Run) error) {
	if _, err := s.Store.UpdateRun(runID, fn); err != nil {
		log.Printf("run %s update failed: %v", runID, err)
	}
}

func (s *Server) requestRunCancelBestEffort(runID, reason, source string) {
	if err := s.Store.RequestRunCancel(runID, reason, source); err != nil {
		log.Printf("run %s request cancel failed: %v", runID, err)
	}
}

func (s *Server) removeRunExecutionBestEffort(ctx context.Context, handle runner.ExecutionHandle, runID, note string) {
	err := s.Launcher.Remove(ctx, handle)
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
		s.cancelQueuedRunsBestEffort(run.TaskID, "task completed; canceled pending runs")
	}

	if !s.isDraining() {
		s.ScheduleRuns(run.TaskID)
	}
}

func InstructionText(run state.Run) string {
	var b strings.Builder
	_, _ = fmt.Fprintf(&b, `# Rascal Run Instructions

Run ID: %s
Task ID: %s
Repository: %s
`, run.ID, run.TaskID, run.Repo)
	if run.IssueNumber > 0 {
		_, _ = fmt.Fprintf(&b, "Issue: #%d\n", run.IssueNumber)
	}
	if run.PRNumber > 0 {
		_, _ = fmt.Fprintf(&b, "Pull Request: #%d\n", run.PRNumber)
	}
	b.WriteString(`
## Task

`)
	b.WriteString(run.Instruction)
	b.WriteString(`

`)
	if shouldIncludeGitContext(run) {
		b.WriteString(`## Git Context

- Remote: ` + "`origin`" + `
- Base branch: ` + "`" + strings.TrimSpace(run.BaseBranch) + "`" + `
- Head branch: ` + "`" + strings.TrimSpace(run.HeadBranch) + "`" + `
- The repository is already cloned and checked out.
- You may use ` + "`git`" + ` and ` + "`gh`" + ` directly.
- Push only to ` + "`origin`" + ` branch ` + "`" + strings.TrimSpace(run.HeadBranch) + "`" + `.
- If you rewrite history, you must run ` + "`git push --force-with-lease origin HEAD:" + strings.TrimSpace(run.HeadBranch) + "`" + `.
- Otherwise run ` + "`git push origin HEAD:" + strings.TrimSpace(run.HeadBranch) + "`" + `.
- Do not push to any other branch.
- Before finishing, ensure the remote branch is updated and the working tree is clean.
`)
		if requiresAgentManagedPublish(run) {
			b.WriteString(`
- If the request involves rebasing, merge conflict resolution, or other history rewriting, do not rely on the harness to publish those changes for you. Perform the required ` + "`git push`" + ` yourself before finishing.
`)
		}
		b.WriteString(`
`)
	}
	b.WriteString(`
## Constraints

- Do not ask for interactive input.
- Do not require MCP tools.
- Keep changes minimal and scoped to the requested task.
- Run ` + "`make lint`" + ` and ` + "`make test`" + ` before finishing if those targets exist.
- If one of those commands does not exist or cannot run, explain exactly why and run the closest equivalent checks instead.
- If you make changes, write /rascal-meta/commit_message.txt using a conventional commit title on the first line.
- Optionally add a commit body after a blank line in /rascal-meta/commit_message.txt.
`)
	if strings.TrimSpace(run.Context) != "" {
		b.WriteString(`
## Additional Context

`)
		b.WriteString(run.Context)
		b.WriteString(`
`)
	}
	return b.String()
}

func shouldIncludeGitContext(run state.Run) bool {
	return run.PRNumber > 0 && strings.TrimSpace(run.BaseBranch) != "" && strings.TrimSpace(run.HeadBranch) != ""
}

func requiresAgentManagedPublish(run state.Run) bool {
	return runtrigger.Normalize(run.Trigger.String()).IsComment()
}

func BuildHeadBranch(taskID, task, runID string) string {
	source := strings.ToLower(strings.TrimSpace(taskID))
	if source == "" || strings.HasPrefix(source, "run_") || strings.HasPrefix(source, "task_") {
		lines := strings.Split(strings.TrimSpace(task), "\n")
		for _, line := range lines {
			line = strings.ToLower(strings.TrimSpace(line))
			if line != "" {
				source = line
				break
			}
		}
	}
	if source == "" {
		source = "task"
	}

	var cleaned strings.Builder
	for _, r := range source {
		switch {
		case r >= 'a' && r <= 'z':
			cleaned.WriteRune(r)
		case r >= '0' && r <= '9':
			cleaned.WriteRune(r)
		case r == '-' || r == '_' || r == '/':
			cleaned.WriteRune(r)
		default:
			cleaned.WriteByte('-')
		}
	}
	taskPart := strings.Trim(cleaned.String(), "-/_")
	if taskPart == "" {
		taskPart = "task"
	}
	if len(taskPart) > 48 {
		taskPart = taskPart[:48]
		taskPart = strings.Trim(taskPart, "-/_")
	}
	runSuffix := strings.TrimSpace(strings.TrimPrefix(runID, "run_"))
	if runSuffix == "" {
		runSuffix = strings.TrimSpace(runID)
	}
	if runSuffix == "" {
		runSuffix = "run"
	}
	if len(runSuffix) > 10 {
		runSuffix = runSuffix[:10]
	}
	return fmt.Sprintf("rascal/%s-%s", taskPart, runSuffix)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func boolPtr(v bool) *bool {
	return &v
}

func isCredentialAuthFailure(errText string) bool {
	text := strings.ToLower(strings.TrimSpace(errText))
	if text == "" {
		return false
	}
	for _, marker := range []string{
		"unauthorized",
		"invalid api key",
		"invalid token",
		"authentication failed",
		"forbidden",
		"permission denied",
	} {
		if strings.Contains(text, marker) {
			return true
		}
	}
	return false
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

func (s *Server) pendingRunCancelReason(runID string) (string, bool) {
	req, ok := s.Store.GetRunCancel(runID)
	if !ok {
		return "", false
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "canceled"
	}
	return reason, true
}

func isCommentTriggeredRun(trigger runtrigger.Name) bool {
	return runtrigger.Normalize(trigger.String()).IsComment()
}
