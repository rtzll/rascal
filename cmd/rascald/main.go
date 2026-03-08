package main

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/defaults"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
	"github.com/rtzll/rascal/internal/state"
)

var errTaskCompleted = errors.New("task is already completed")
var errServerDraining = errors.New("orchestrator is draining")

const runLeaseTTL = 90 * time.Second
const runSupervisorTick = 1 * time.Second
const runResponseTargetFile = "response_target.json"
const runCompletionCommentMarkerFile = "completion_comment_posted.json"
const runCompletionCommentBodyMarker = "<!-- rascal:completion-comment -->"
const agentLogFile = "agent.ndjson"
const legacyAgentLogFile = "goose.ndjson"

type githubClient interface {
	GetIssue(ctx context.Context, repo string, issueNumber int) (ghapi.IssueData, error)
	AddIssueReaction(ctx context.Context, repo string, issueNumber int, content string) error
	RemoveIssueReactions(ctx context.Context, repo string, issueNumber int) error
	AddIssueCommentReaction(ctx context.Context, repo string, commentID int64, content string) error
	AddPullRequestReviewReaction(ctx context.Context, repo string, pullNumber int, reviewID int64, content string) error
	AddPullRequestReviewCommentReaction(ctx context.Context, repo string, commentID int64, content string) error
	CreateIssueComment(ctx context.Context, repo string, issueNumber int, body string) error
}

type server struct {
	cfg      config.ServerConfig
	store    *state.Store
	launcher runner.Launcher
	gh       githubClient

	mu            sync.Mutex
	runCancels    map[string]context.CancelFunc
	scheduleMu    sync.Mutex
	maxConcurrent int
	draining      bool
	instanceID    string
}

type runRequest struct {
	TaskID      string
	Repo        string
	Task        string
	BaseBranch  string
	HeadBranch  string
	Trigger     string
	IssueNumber int
	PRNumber    int
	PRStatus    state.PRStatus
	Context     string
	Debug       *bool

	ResponseTarget *runResponseTarget
}

type runResponseTarget struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	RequestedBy string `json:"requested_by,omitempty"`
	Trigger     string `json:"trigger"`
}

type runCompletionCommentMarker struct {
	RunID       string `json:"run_id"`
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	PostedAt    string `json:"posted_at"`
}

type createTaskRequest struct {
	TaskID     string `json:"task_id"`
	Repo       string `json:"repo"`
	Task       string `json:"task"`
	BaseBranch string `json:"base_branch"`
	Trigger    string `json:"trigger,omitempty"`
	Debug      *bool  `json:"debug,omitempty"`
}

type createIssueTaskRequest struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
	Debug       *bool  `json:"debug,omitempty"`
}

type requestIDKey struct{}

func main() {
	cfg := config.LoadServerConfig()
	if err := cfg.Ensure(); err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := state.New(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	s := &server{
		cfg:           cfg,
		store:         store,
		launcher:      runner.NewLauncher(cfg.RunnerMode, cfg.RunnerImageForBackend(cfg.AgentBackend), cfg.GitHubToken),
		gh:            ghapi.NewAPIClient(cfg.GitHubToken),
		runCancels:    make(map[string]context.CancelFunc),
		maxConcurrent: defaultMaxConcurrent(),
		instanceID:    fmt.Sprintf("%s-%d-%d", strings.TrimSpace(cfg.Slot), os.Getpid(), time.Now().UTC().UnixNano()),
	}
	s.recoverQueuedCancels()
	s.recoverRunningRuns()
	s.scheduleRuns("")

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/readyz", s.handleReady)
	mux.HandleFunc("/v1/runs", s.withAuth(s.handleListRuns))
	mux.HandleFunc("/v1/runs/", s.withAuth(s.handleRunSubresources))
	mux.HandleFunc("/v1/tasks", s.withAuth(s.handleCreateTask))
	mux.HandleFunc("/v1/tasks/", s.withAuth(s.handleTaskSubresources))
	mux.HandleFunc("/v1/tasks/issue", s.withAuth(s.handleCreateIssueTask))
	mux.HandleFunc("/v1/webhooks/github", s.handleWebhook)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           withRequestID(logRequests(mux)),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("rascald listening on %s (runner=%s backend=%s)", cfg.ListenAddr, cfg.RunnerMode, cfg.AgentBackend)
	serverErr := make(chan error, 1)
	go func() {
		serverErr <- httpServer.ListenAndServe()
	}()

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	select {
	case err := <-serverErr:
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Fatalf("server stopped: %v", err)
		}
		return
	case <-sigCtx.Done():
	}

	log.Printf("shutdown signal received; entering drain mode")
	s.beginDrain()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := httpServer.Shutdown(shutdownCtx); err != nil && !errors.Is(err, context.Canceled) {
		log.Printf("http shutdown warning: %v", err)
	}

	s.stopRunSupervisors()
	if err := s.waitForNoActiveRuns(10 * time.Second); err != nil {
		log.Printf("shutdown exiting with active detached runs still executing: %v", err)
	}
}

func (s *server) recoverQueuedCancels() {
	runs := s.store.ListRuns(10000)
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

func (s *server) recoverRunningRuns() {
	now := time.Now().UTC()
	runs := s.store.ListRunningRuns()
	for _, run := range runs {
		if exec, ok := s.store.GetRunExecution(run.ID); ok {
			s.recoverDetachedRun(run, exec)
			continue
		}
		if reason, ok := s.pendingRunCancelReason(run.ID); ok {
			s.setRunStatusBestEffort(run.ID, state.StatusCanceled, reason)
			s.clearRunCancelBestEffort(run.ID)
			continue
		}

		lease, hasLease := s.store.GetRunLease(run.ID)
		if hasLease {
			if lease.LeaseExpiresAt.After(now) {
				continue
			}
			s.deleteRunLeaseBestEffort(run.ID)
			if err := s.requeueRun(run.ID); err != nil {
				log.Printf("recover run %s after expired lease: %v", run.ID, err)
			}
			continue
		}

		// If there is no lease yet but start time is very recent, keep current
		// state to avoid racing an in-flight lease write.
		if run.StartedAt != nil && run.StartedAt.After(now.Add(-runLeaseTTL)) {
			continue
		}
		if err := s.requeueRun(run.ID); err != nil {
			log.Printf("recover run %s without lease: %v", run.ID, err)
		}
	}
}

func (s *server) recoverDetachedRun(run state.Run, execRec state.RunExecution) {
	handle := runExecutionHandle(execRec)
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	execState, err := s.launcher.Inspect(inspectCtx, handle)
	switch {
	case errors.Is(err, runner.ErrExecutionNotFound):
		s.failRunForMissingExecution(run, "detached container missing during adoption")
		return
	case err != nil:
		log.Printf("recover run %s inspect failed, adopting with retry loop: %v", run.ID, err)
		if err := s.store.UpsertRunLease(run.ID, s.instanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec)
		return
	}

	if execState.Running {
		if _, err := s.store.UpdateRunExecutionState(run.ID, "running", 0, time.Now().UTC()); err != nil {
			log.Printf("recover run %s update execution running state failed: %v", run.ID, err)
		}
		if err := s.store.UpsertRunLease(run.ID, s.instanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec)
		return
	}

	exitCode := 0
	if execState.ExitCode != nil {
		exitCode = *execState.ExitCode
	}
	if _, err := s.store.UpdateRunExecutionState(run.ID, "exited", exitCode, time.Now().UTC()); err != nil {
		log.Printf("recover run %s update execution exited state failed: %v", run.ID, err)
	}
	s.finalizeDetachedRun(run.ID, execRec, exitCode)
}

func (s *server) failRunForMissingExecution(run state.Run, reason string) {
	updated := s.setRunStatusWithFallback(run, state.StatusFailed, reason)
	s.deleteRunExecutionBestEffort(run.ID)
	s.deleteRunLeaseBestEffort(run.ID)
	s.finishRun(updated)
}

func (s *server) withAuth(next http.HandlerFunc) http.HandlerFunc {
	if !s.cfg.AuthEnabled() {
		return next
	}

	return func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const bearer = "Bearer "
		if !strings.HasPrefix(auth, bearer) {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(auth, bearer)
		if subtle.ConstantTimeCompare([]byte(provided), []byte(s.cfg.APIToken)) != 1 {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		next(w, r)
	}
}

func (s *server) handleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "rascald", "ready": !s.isDraining()})
}

func (s *server) handleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		writeJSON(w, http.StatusServiceUnavailable, map[string]any{"ok": false, "service": "rascald", "ready": false})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "rascald", "ready": true})
}

func (s *server) handleListRuns(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	all := false
	if raw := strings.TrimSpace(r.URL.Query().Get("all")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			http.Error(w, "invalid all", http.StatusBadRequest)
			return
		}
		all = parsed
	}

	limit := 50
	if !all {
		if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
			parsed, err := strconv.Atoi(raw)
			if err != nil || parsed <= 0 {
				http.Error(w, "invalid limit", http.StatusBadRequest)
				return
			}
			limit = parsed
		}
	} else {
		limit = 0
	}

	writeJSON(w, http.StatusOK, map[string]any{"runs": s.store.ListRuns(limit)})
}

func (s *server) handleCreateTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}

	var req createTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.Repo = strings.TrimSpace(req.Repo)
	req.Task = strings.TrimSpace(req.Task)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	req.Trigger = strings.TrimSpace(req.Trigger)
	if req.Repo == "" || req.Task == "" {
		http.Error(w, "repo and task are required", http.StatusBadRequest)
		return
	}

	run, err := s.createAndQueueRun(runRequest{
		TaskID:     req.TaskID,
		Repo:       req.Repo,
		Task:       req.Task,
		BaseBranch: req.BaseBranch,
		Trigger:    req.Trigger,
		Debug:      req.Debug,
	})
	if err != nil {
		if errors.Is(err, errServerDraining) {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to create run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *server) handleCreateIssueTask(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}

	var req createIssueTaskRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body", http.StatusBadRequest)
		return
	}
	req.Repo = strings.TrimSpace(req.Repo)
	if req.Repo == "" || req.IssueNumber <= 0 {
		http.Error(w, "repo and issue_number are required", http.StatusBadRequest)
		return
	}

	taskID := fmt.Sprintf("%s#%d", req.Repo, req.IssueNumber)
	taskText := fmt.Sprintf("Work on issue #%d in %s", req.IssueNumber, req.Repo)
	ctxText := ""
	if strings.TrimSpace(s.cfg.GitHubToken) != "" {
		issue, err := s.gh.GetIssue(r.Context(), req.Repo, req.IssueNumber)
		if err != nil {
			http.Error(w, "failed to fetch issue: "+err.Error(), http.StatusBadGateway)
			return
		}
		taskText = issueTaskFromIssue(issue.Title, issue.Body)
		ctxText = fmt.Sprintf("Issue URL: %s", issue.HTMLURL)
	}

	run, err := s.createAndQueueRun(runRequest{
		TaskID:      taskID,
		Repo:        req.Repo,
		Task:        taskText,
		Trigger:     "issue_api",
		IssueNumber: req.IssueNumber,
		Context:     ctxText,
		Debug:       req.Debug,
	})
	if err != nil {
		if errors.Is(err, errTaskCompleted) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
			return
		}
		if errors.Is(err, errServerDraining) {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to create run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]any{"run": run})
}

func (s *server) handleTaskSubresources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	path := strings.TrimPrefix(r.URL.Path, "/v1/tasks/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "task id is required", http.StatusBadRequest)
		return
	}
	taskID, err := url.PathUnescape(path)
	if err != nil {
		http.Error(w, "invalid task id", http.StatusBadRequest)
		return
	}
	task, ok := s.store.GetTask(taskID)
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"task": task})
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		http.Error(w, "server is draining", http.StatusServiceUnavailable)
		return
	}
	if !s.isActiveWebhookSlot() {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": false, "inactive_slot": true})
		return
	}

	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return
	}

	if secret := strings.TrimSpace(s.cfg.GitHubWebhookSecret); secret != "" {
		sig := r.Header.Get("X-Hub-Signature-256")
		if !ghapi.VerifySignatureSHA256([]byte(secret), payload, sig) {
			http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
			return
		}
	}

	deliveryID := ghapi.DeliveryID(r.Header)
	var deliveryClaim state.DeliveryClaim
	if deliveryID != "" {
		claim, claimed, claimErr := s.store.ClaimDelivery(deliveryID, s.instanceID)
		if claimErr != nil {
			http.Error(w, "failed to claim delivery id", http.StatusInternalServerError)
			return
		}
		if !claimed {
			writeJSON(w, http.StatusOK, map[string]any{"duplicate": true})
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
		if err := s.store.CompleteDeliveryClaim(deliveryClaim); err != nil {
			http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
			return
		}
	}

	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *server) processWebhookEvent(ctx context.Context, eventType string, payload []byte) error {
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
			taskID := fmt.Sprintf("%s#%d", ev.Repository.FullName, ev.Issue.Number)
			_, err := s.createAndQueueRun(runRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Task:        issueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     "issue_label",
				IssueNumber: ev.Issue.Number,
				Context:     fmt.Sprintf("Triggered by label 'rascal' on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
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
			if !issueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := fmt.Sprintf("%s#%d", ev.Repository.FullName, ev.Issue.Number)
			if err := s.store.CancelQueuedRuns(taskID, "issue edited"); err != nil {
				return err
			}
			_, err := s.createAndQueueRun(runRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Task:        issueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     "issue_edited",
				IssueNumber: ev.Issue.Number,
				Context:     fmt.Sprintf("Triggered by issue edit on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
			})
			if errors.Is(err, errTaskCompleted) {
				return nil
			}
			return err
		case "closed":
			if !issueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := fmt.Sprintf("%s#%d", ev.Repository.FullName, ev.Issue.Number)
			if _, err := s.store.UpsertTask(state.UpsertTaskInput{
				ID:          taskID,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			}); err != nil {
				return err
			}
			if err := s.store.MarkTaskCompleted(taskID); err != nil {
				return err
			}
			if err := s.store.CancelQueuedRuns(taskID, "issue closed"); err != nil {
				return err
			}
			s.cancelRunningTaskRuns(taskID, "issue closed")
			return nil
		case "reopened":
			if !issueHasLabel(ev.Issue.Labels, "rascal") {
				return nil
			}
			taskID := fmt.Sprintf("%s#%d", ev.Repository.FullName, ev.Issue.Number)
			if _, err := s.store.UpsertTask(state.UpsertTaskInput{
				ID:          taskID,
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
			}); err != nil {
				return err
			}
			if err := s.store.MarkTaskOpen(taskID); err != nil {
				return err
			}
			_, err := s.createAndQueueRun(runRequest{
				TaskID:      taskID,
				Repo:        ev.Repository.FullName,
				Task:        issueTaskFromIssue(ev.Issue.Title, ev.Issue.Body),
				Trigger:     "issue_reopened",
				IssueNumber: ev.Issue.Number,
				PRStatus:    state.PRStatusNone,
				Context:     fmt.Sprintf("Triggered by issue reopen on issue #%d", ev.Issue.Number),
				Debug:       boolPtr(true),
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
			if !issueCommentBodyChanged(ev) {
				return nil
			}
		default:
			return nil
		}
		if isRascalAutomationComment(ev.Comment.Body) {
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

		_, err := s.createAndQueueRun(runRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Task:        fmt.Sprintf("Address PR #%d feedback", ev.Issue.Number),
			Trigger:     "pr_comment",
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.Issue.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     strings.TrimSpace(ev.Comment.Body),
			BaseBranch:  s.defaultBaseBranchForTask(task.ID),
			HeadBranch:  s.defaultHeadBranchForTask(task.ID),
			Debug:       boolPtr(true),
			ResponseTarget: &runResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.Issue.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     "pr_comment",
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

		contextText := strings.TrimSpace(ev.Review.Body)
		if contextText == "" {
			contextText = fmt.Sprintf("review state: %s", ev.Review.State)
		}
		_, err := s.createAndQueueRun(runRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Task:        fmt.Sprintf("Address PR #%d review feedback", ev.PullRequest.Number),
			Trigger:     "pr_review",
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.PullRequest.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     contextText,
			BaseBranch:  s.defaultBaseBranchForTask(task.ID),
			HeadBranch:  s.defaultHeadBranchForTask(task.ID),
			Debug:       boolPtr(true),
			ResponseTarget: &runResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.PullRequest.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     "pr_review",
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
			if !reviewCommentBodyChanged(ev) {
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

		contextText := strings.TrimSpace(ev.Comment.Body)
		if location := formatReviewCommentLocation(ev.Comment.Path, ev.Comment.StartLine, ev.Comment.Line); location != "" {
			if contextText == "" {
				contextText = fmt.Sprintf("inline review comment at %s", location)
			} else {
				contextText = fmt.Sprintf(`%s

Inline comment location: %s`, contextText, location)
			}
		}
		_, err := s.createAndQueueRun(runRequest{
			TaskID:      task.ID,
			Repo:        ev.Repository.FullName,
			Task:        fmt.Sprintf("Address PR #%d inline review comment", ev.PullRequest.Number),
			Trigger:     "pr_review_comment",
			IssueNumber: task.IssueNumber,
			PRNumber:    ev.PullRequest.Number,
			PRStatus:    state.PRStatusOpen,
			Context:     contextText,
			BaseBranch:  s.defaultBaseBranchForTask(task.ID),
			HeadBranch:  s.defaultHeadBranchForTask(task.ID),
			Debug:       boolPtr(true),
			ResponseTarget: &runResponseTarget{
				Repo:        ev.Repository.FullName,
				IssueNumber: ev.PullRequest.Number,
				RequestedBy: strings.TrimSpace(ev.Sender.Login),
				Trigger:     "pr_review_comment",
			},
		})
		if errors.Is(err, errTaskCompleted) {
			return nil
		}
		return err
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
				if err := s.store.MarkTaskCompleted(taskID); err != nil {
					return err
				}
				if err := s.store.CancelQueuedRuns(taskID, "task completed by merged PR"); err != nil {
					return err
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

func (s *server) handleRunSubresources(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "run id is required", http.StatusBadRequest)
		return
	}

	switch {
	case strings.HasSuffix(path, "/logs"):
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		runID := strings.TrimSuffix(path, "/logs")
		runID = strings.Trim(runID, "/")
		s.handleRunLogs(w, r, runID)
		return
	case strings.HasSuffix(path, "/cancel"):
		if r.Method != http.MethodPost {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		runID := strings.TrimSuffix(path, "/cancel")
		runID = strings.Trim(runID, "/")
		s.handleCancelRun(w, runID)
		return
	default:
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.handleGetRun(w, path)
		return
	}
}

func (s *server) handleGetRun(w http.ResponseWriter, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *server) handleCancelRun(w http.ResponseWriter, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.Status == state.StatusSucceeded || run.Status == state.StatusFailed || run.Status == state.StatusCanceled {
		s.clearRunCancelBestEffort(runID)
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "canceled": false, "reason": "run already completed"})
		return
	}
	if err := s.store.RequestRunCancel(runID, "canceled by user", "user"); err != nil {
		http.Error(w, "failed to persist cancel request", http.StatusInternalServerError)
		return
	}

	if run.Status == state.StatusQueued {
		updated, err := s.store.SetRunStatus(runID, state.StatusCanceled, "canceled by user")
		if err != nil {
			http.Error(w, "failed to cancel run", http.StatusInternalServerError)
			return
		}
		s.clearRunCancelBestEffort(runID)
		if !s.isDraining() {
			s.scheduleRuns(run.TaskID)
		}
		writeJSON(w, http.StatusOK, map[string]any{"run": updated, "canceled": true})
		return
	}

	s.stopRunExecutionBestEffort(runID, "user cancel requested")
	writeJSON(w, http.StatusAccepted, map[string]any{"run_id": runID, "cancel_requested": true})
}

func (s *server) handleRunLogs(w http.ResponseWriter, r *http.Request, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	lines := 200
	if raw := strings.TrimSpace(r.URL.Query().Get("lines")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid lines", http.StatusBadRequest)
			return
		}
		if parsed > 5000 {
			parsed = 5000
		}
		lines = parsed
	}

	runnerLines, err := logs.Tail(filepath.Join(run.RunDir, "runner.log"), lines)
	runnerNote := ""
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			runnerNote = "(runner.log not found)"
		} else {
			runnerNote = "(runner.log unavailable)"
		}
	}
	agentLines, agentNote := tailRunAgentLog(run.RunDir, lines)

	var body strings.Builder
	_, _ = fmt.Fprintln(&body, "== runner.log ==")
	for _, line := range runnerLines {
		_, _ = fmt.Fprintln(&body, line)
	}
	if runnerNote != "" {
		_, _ = fmt.Fprintln(&body, runnerNote)
	}
	_, _ = fmt.Fprintln(&body, `
== agent.ndjson ==`)
	for _, line := range agentLines {
		_, _ = fmt.Fprintln(&body, line)
	}
	if agentNote != "" {
		_, _ = fmt.Fprintln(&body, agentNote)
	}

	logsText := body.String()
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "", "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		if _, err := io.WriteString(w, logsText); err != nil {
			log.Printf("write logs response failed: %v", err)
		}
	case "json":
		writeJSON(w, http.StatusOK, map[string]any{
			"logs":       logsText,
			"run_status": run.Status,
			"done":       runStatusIsDone(run.Status),
		})
	default:
		http.Error(w, "invalid format", http.StatusBadRequest)
	}
}

func runStatusIsDone(status state.RunStatus) bool {
	return state.IsFinalRunStatus(status)
}

func (s *server) createAndQueueRun(req runRequest) (state.Run, error) {
	if s.isDraining() {
		return state.Run{}, errServerDraining
	}
	req.Repo = strings.TrimSpace(req.Repo)
	req.Task = strings.TrimSpace(req.Task)
	req.TaskID = strings.TrimSpace(req.TaskID)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	req.HeadBranch = strings.TrimSpace(req.HeadBranch)
	req.Context = strings.TrimSpace(req.Context)
	if req.Repo == "" || req.Task == "" {
		return state.Run{}, fmt.Errorf("repo and task are required")
	}
	if req.Trigger == "" {
		req.Trigger = "cli"
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
		return state.Run{}, err
	}
	if req.TaskID == "" {
		req.TaskID = runID
	}
	if s.store.IsTaskCompleted(req.TaskID) {
		return state.Run{}, errTaskCompleted
	}

	lastRun, hasLastRun := s.store.LastRunForTask(req.TaskID)
	if req.BaseBranch == "" {
		if hasLastRun && lastRun.BaseBranch != "" {
			req.BaseBranch = lastRun.BaseBranch
		} else {
			req.BaseBranch = "main"
		}
	}
	if req.HeadBranch == "" {
		if hasLastRun && (req.Trigger == "pr_comment" || req.Trigger == "pr_review") && lastRun.HeadBranch != "" {
			req.HeadBranch = lastRun.HeadBranch
		} else {
			req.HeadBranch = buildHeadBranch(req.TaskID, req.Task, runID)
		}
	}

	runDir := filepath.Join(s.cfg.DataDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		return state.Run{}, fmt.Errorf("create run dir: %w", err)
	}

	_, err = s.store.UpsertTask(state.UpsertTaskInput{
		ID:           req.TaskID,
		Repo:         req.Repo,
		AgentBackend: s.cfg.AgentBackend,
		IssueNumber:  req.IssueNumber,
		PRNumber:     req.PRNumber,
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("upsert task: %w", err)
	}

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:           runID,
		TaskID:       req.TaskID,
		Repo:         req.Repo,
		Task:         req.Task,
		AgentBackend: s.cfg.AgentBackend,
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

	if err := s.writeRunFiles(run); err != nil {
		s.setRunStatusBestEffort(run.ID, state.StatusFailed, err.Error())
		return state.Run{}, fmt.Errorf("prepare run files: %w", err)
	}
	if err := s.writeRunResponseTarget(run, req.ResponseTarget); err != nil {
		s.setRunStatusBestEffort(run.ID, state.StatusFailed, err.Error())
		return state.Run{}, fmt.Errorf("prepare run response target: %w", err)
	}
	s.scheduleRuns(run.TaskID)
	return run, nil
}

func (s *server) writeRunFiles(run state.Run) (err error) {
	if err := os.MkdirAll(filepath.Join(run.RunDir, "codex"), 0o755); err != nil {
		return err
	}
	if strings.TrimSpace(s.cfg.CodexAuthPath) != "" {
		if _, err := os.Stat(s.cfg.CodexAuthPath); err == nil {
			if err := copyFile(s.cfg.CodexAuthPath, filepath.Join(run.RunDir, "codex", "auth.json"), 0o600); err != nil {
				return fmt.Errorf("copy codex auth: %w", err)
			}
		}
	}

	ctxPayload := map[string]any{
		"run_id":       run.ID,
		"task_id":      run.TaskID,
		"repo":         run.Repo,
		"task":         run.Task,
		"trigger":      run.Trigger,
		"issue_number": run.IssueNumber,
		"pr_number":    run.PRNumber,
		"context":      run.Context,
		"debug":        run.Debug,
	}
	ctxData, err := json.MarshalIndent(ctxPayload, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "context.json"), ctxData, 0o644); err != nil {
		return err
	}

	instructions := instructionText(run)
	if err := os.WriteFile(filepath.Join(run.RunDir, "instructions.md"), []byte(instructions), 0o644); err != nil {
		return err
	}

	logLine := fmt.Sprintf("[%s] queued run=%s task=%s trigger=%s\n", time.Now().UTC().Format(time.RFC3339), run.ID, run.TaskID, run.Trigger)
	f, err := os.OpenFile(filepath.Join(run.RunDir, "runner.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := f.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()
	_, err = f.WriteString(logLine)
	return err
}

func (s *server) writeRunResponseTarget(run state.Run, target *runResponseTarget) error {
	if target == nil {
		return nil
	}
	out := runResponseTarget{
		Repo:        strings.TrimSpace(target.Repo),
		IssueNumber: target.IssueNumber,
		RequestedBy: strings.TrimSpace(target.RequestedBy),
		Trigger:     strings.TrimSpace(target.Trigger),
	}
	if out.Repo == "" {
		out.Repo = strings.TrimSpace(run.Repo)
	}
	if out.IssueNumber <= 0 {
		out.IssueNumber = run.PRNumber
	}
	if out.Trigger == "" {
		out.Trigger = strings.TrimSpace(run.Trigger)
	}
	if out.Repo == "" || out.IssueNumber <= 0 {
		return nil
	}

	data, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return fmt.Errorf("encode run response target: %w", err)
	}
	path := filepath.Join(run.RunDir, runResponseTargetFile)
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write run response target: %w", err)
	}
	return nil
}

func (s *server) executeRun(runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		return
	}
	if reason, ok := s.pendingRunCancelReason(runID); ok {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, reason)
		s.finishRun(updated)
		return
	}

	if s.store.IsTaskCompleted(run.TaskID) {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, "task is already completed")
		s.finishRun(updated)
		return
	}

	if run.Status == state.StatusQueued {
		claimedRun, claimed, err := s.store.ClaimRunStart(runID)
		if err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
			s.finishRun(updated)
			return
		}
		run = claimedRun
		if !claimed {
			if run.Status != state.StatusQueued {
				s.finishRun(run)
				return
			}
			return
		}
	}
	if run.Status != state.StatusRunning {
		s.finishRun(run)
		return
	}
	s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionEyes)

	if err := s.store.UpsertRunLease(run.ID, s.instanceID, runLeaseTTL); err != nil {
		updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("claim run lease: %v", err))
		s.finishRun(updated)
		return
	}
	defer func() {
		if err := s.store.DeleteRunLeaseForOwner(run.ID, s.instanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", run.ID, err)
		}
	}()

	if reason, ok := s.pendingRunCancelReason(runID); ok {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, reason)
		s.finishRun(updated)
		return
	}

	sessionMode := s.cfg.EffectiveAgentSessionMode()
	if sessionMode != agent.SessionModeOff {
		s.cleanupAgentSessionsBestEffort()
	}

	sessionResume := agent.SessionEnabled(sessionMode, run.Trigger)
	sessionTaskKey := ""
	sessionTaskDir := ""
	backendSessionID := ""
	sessionRoot := strings.TrimSpace(s.cfg.EffectiveAgentSessionRoot())
	if sessionRoot == "" {
		sessionRoot = filepath.Join(s.cfg.DataDir, defaults.AgentSessionDirName)
	}
	if sessionResume {
		sessionTaskKey = agent.SessionTaskKey(run.Repo, run.TaskID)
		sessionTaskDir = filepath.Join(sessionRoot, sessionTaskKey)
		if existing, ok := s.store.GetTaskAgentSession(run.TaskID); ok {
			backendSessionID = strings.TrimSpace(existing.BackendSessionID)
		}
		if backendSessionID == "" && run.AgentBackend == agent.BackendGoose {
			backendSessionID = runner.GooseSessionName(run.Repo, run.TaskID)
		}
		if err := os.MkdirAll(sessionTaskDir, 0o755); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("create agent session dir: %v", err))
			s.finishRun(updated)
			return
		}
		if _, err := s.store.UpsertTaskAgentSession(state.UpsertTaskAgentSessionInput{
			TaskID:           run.TaskID,
			AgentBackend:     run.AgentBackend,
			BackendSessionID: backendSessionID,
			SessionKey:       sessionTaskKey,
			SessionRoot:      sessionTaskDir,
			LastRunID:        run.ID,
		}); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist agent session: %v", err))
			s.finishRun(updated)
			return
		}
	}

	spec := runner.Spec{
		RunID:        run.ID,
		TaskID:       run.TaskID,
		Repo:         run.Repo,
		Task:         run.Task,
		AgentBackend: run.AgentBackend,
		RunnerImage:  s.cfg.RunnerImageForBackend(run.AgentBackend),
		BaseBranch:   run.BaseBranch,
		HeadBranch:   run.HeadBranch,
		Trigger:      run.Trigger,
		RunDir:       run.RunDir,
		IssueNumber:  run.IssueNumber,
		PRNumber:     run.PRNumber,
		Context:      run.Context,
		Debug:        run.Debug,
		AgentSession: runner.SessionSpec{
			Mode:             sessionMode,
			Resume:           sessionResume,
			TaskDir:          sessionTaskDir,
			TaskKey:          sessionTaskKey,
			BackendSessionID: backendSessionID,
		},
		GooseSessionMode:    string(sessionMode),
		GooseSessionResume:  sessionResume,
		GooseSessionTaskDir: sessionTaskDir,
		GooseSessionTaskKey: sessionTaskKey,
		GooseSessionName:    backendSessionID,
	}
	log.Printf("run %s backend=%s session_mode=%s resume=%t key=%s session_id=%s", run.ID, run.AgentBackend, sessionMode, sessionResume, sessionTaskKey, backendSessionID)
	execRec, hasExec := s.store.GetRunExecution(run.ID)
	if !hasExec {
		// Persist a deterministic handle before launch so the next slot can
		// adopt the container even if this process exits mid-startup.
		pendingHandle := runner.ExecutionHandleForRun(run.ID)
		if _, err := s.store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       pendingHandle.Backend,
			ContainerName: pendingHandle.Name,
			ContainerID:   pendingHandle.Name,
			Status:        "created",
			ExitCode:      0,
		}); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist run execution: %v", err))
			s.finishRun(updated)
			return
		}

		handle, err := s.startDetachedWithRetry(context.Background(), spec)
		if err != nil {
			s.deleteRunExecutionBestEffort(run.ID)
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
			s.finishRun(updated)
			return
		}
		execRec, err = s.store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       strings.TrimSpace(handle.Backend),
			ContainerName: strings.TrimSpace(handle.Name),
			ContainerID:   strings.TrimSpace(handle.ID),
			Status:        "running",
			ExitCode:      0,
		})
		if err != nil {
			s.stopRunExecutionBestEffort(run.ID, "failed to persist run execution")
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
			s.removeRunExecutionBestEffort(stopCtx, handle, run.ID, "cleanup failed persisted execution")
			stopCancel()
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist run execution: %v", err))
			s.finishRun(updated)
			return
		}
	}

	s.superviseDetachedRunLoop(run.ID, execRec)
}

func (s *server) superviseDetachedRunLoop(runID string, execRec state.RunExecution) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if _, exists := s.runCancels[runID]; exists {
		s.mu.Unlock()
		cancel()
		return
	}
	s.runCancels[runID] = cancel
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		delete(s.runCancels, runID)
		s.mu.Unlock()
		if err := s.store.DeleteRunLeaseForOwner(runID, s.instanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", runID, err)
		}
	}()

	s.superviseRun(ctx, runID, execRec)
}

func (s *server) superviseRun(ctx context.Context, runID string, execRec state.RunExecution) {
	interval := runSupervisorTick
	if interval <= 0 {
		interval = time.Second
	}
	renewEvery := runLeaseTTL / 3
	if renewEvery <= 0 {
		renewEvery = 30 * time.Second
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	nextRenewAt := time.Now().UTC().Add(renewEvery)
	stopRequested := false
	handle := runExecutionHandle(execRec)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if time.Now().UTC().Before(nextRenewAt) {
				// Continue with inspect/cancel handling on every tick.
			} else {
				ok, err := s.store.RenewRunLease(runID, s.instanceID, runLeaseTTL)
				if err != nil {
					log.Printf("run %s lease heartbeat failed: %v", runID, err)
					nextRenewAt = time.Now().UTC().Add(renewEvery)
					continue
				}
				if !ok {
					log.Printf("run %s lease ownership lost; stopping local supervision", runID)
					return
				}
				nextRenewAt = time.Now().UTC().Add(renewEvery)
			}

			now := time.Now().UTC()
			execState, err := s.launcher.Inspect(ctx, handle)
			if errors.Is(err, runner.ErrExecutionNotFound) {
				run, ok := s.store.GetRun(runID)
				if ok {
					s.failRunForMissingExecution(run, "detached container missing during adoption")
				}
				return
			}
			if err != nil {
				if !errors.Is(err, context.Canceled) {
					log.Printf("run %s inspect failed: %v", runID, err)
				}
				continue
			}

			if execState.Running {
				execStatus := "running"
				if reason, ok := s.pendingRunCancelReason(runID); ok {
					execStatus = "stopping"
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := s.launcher.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop failed: %v", runID, stopErr)
						}
						log.Printf("run %s cancel requested: %s", runID, reason)
						stopRequested = true
					}
				}
				if _, err := s.store.UpdateRunExecutionState(runID, execStatus, 0, now); err != nil {
					log.Printf("run %s update execution state %q failed: %v", runID, execStatus, err)
				}
				continue
			}

			exitCode := 0
			if execState.ExitCode != nil {
				exitCode = *execState.ExitCode
			}
			if _, err := s.store.UpdateRunExecutionState(runID, "exited", exitCode, now); err != nil {
				log.Printf("run %s update execution exited state failed: %v", runID, err)
			}
			s.finalizeDetachedRun(runID, execRec, exitCode)
			return
		}
	}
}

func (s *server) finalizeDetachedRun(runID string, execRec state.RunExecution, observedExitCode int) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		s.cleanupDetachedExecution(runID, execRec)
		return
	}

	if state.IsFinalRunStatus(run.Status) {
		s.cleanupDetachedExecution(runID, execRec)
		s.finishRun(run)
		return
	}

	metaPath := filepath.Join(run.RunDir, "meta.json")
	meta, metaErr := runner.ReadMeta(metaPath)
	if metaErr != nil {
		meta = runner.Meta{
			RunID:      run.ID,
			TaskID:     run.TaskID,
			Repo:       run.Repo,
			BaseBranch: run.BaseBranch,
			HeadBranch: run.HeadBranch,
			ExitCode:   observedExitCode,
		}
		if observedExitCode != 0 {
			meta.Error = fmt.Sprintf("docker runner failed with exit code %d", observedExitCode)
		}
		if writeErr := runner.WriteMeta(metaPath, meta); writeErr != nil {
			log.Printf("run %s write fallback meta failed: %v", run.ID, writeErr)
		}
	}
	if meta.ExitCode == 0 && observedExitCode != 0 {
		meta.ExitCode = observedExitCode
	}
	if strings.TrimSpace(meta.AgentSessionID) != "" {
		existing, _ := s.store.GetTaskAgentSession(run.TaskID)
		if _, err := s.store.UpsertTaskAgentSession(state.UpsertTaskAgentSessionInput{
			TaskID:           run.TaskID,
			AgentBackend:     run.AgentBackend,
			BackendSessionID: strings.TrimSpace(meta.AgentSessionID),
			SessionKey:       existing.SessionKey,
			SessionRoot:      existing.SessionRoot,
			LastRunID:        run.ID,
		}); err != nil {
			log.Printf("run %s failed to persist resolved agent session id %q: %v", run.ID, meta.AgentSessionID, err)
		}
	}

	status := state.StatusSucceeded
	prStatus := state.PRStatusNone
	errText := ""
	if meta.ExitCode != 0 || strings.TrimSpace(meta.Error) != "" {
		status = state.StatusFailed
		if strings.TrimSpace(meta.Error) != "" {
			errText = strings.TrimSpace(meta.Error)
		} else {
			errText = fmt.Sprintf("docker runner failed with exit code %d", meta.ExitCode)
		}
	} else if meta.PRNumber > 0 || strings.TrimSpace(meta.PRURL) != "" || run.PRNumber > 0 || strings.TrimSpace(run.PRURL) != "" {
		status = state.StatusReview
		prStatus = state.PRStatusOpen
	}
	if reason, canceled := s.pendingRunCancelReason(runID); canceled && status == state.StatusFailed {
		// Cancellation should explain a stopped execution, but it should not
		// overwrite a successful terminal result that raced with the request.
		status = state.StatusCanceled
		errText = reason
	}

	now := time.Now().UTC()
	updated, err := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.Status = status
		r.Error = errText
		r.PRNumber = maxInt(r.PRNumber, meta.PRNumber)
		if strings.TrimSpace(meta.PRURL) != "" {
			r.PRURL = strings.TrimSpace(meta.PRURL)
		}
		if strings.TrimSpace(meta.HeadSHA) != "" {
			r.HeadSHA = strings.TrimSpace(meta.HeadSHA)
		}
		r.PRStatus = prStatus
		r.CompletedAt = &now
		return nil
	})
	if err != nil {
		log.Printf("failed to persist detached run result for %s: %v", run.ID, err)
		updated = s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
	}

	switch updated.Status {
	case state.StatusSucceeded:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionRocket)
	case state.StatusReview:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionHooray)
	case state.StatusFailed:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionConfused)
	case state.StatusCanceled:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionMinusOne)
	}
	if updated.PRNumber > 0 {
		s.setTaskPRBestEffort(updated.TaskID, updated.Repo, updated.PRNumber)
	}
	if updated.Status == state.StatusSucceeded || updated.Status == state.StatusReview {
		s.postRunCompletionCommentBestEffort(updated)
	}

	s.cleanupDetachedExecution(runID, execRec)
	s.finishRun(updated)
}

func (s *server) cleanupDetachedExecution(runID string, execRec state.RunExecution) {
	removeCtx, removeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.launcher.Remove(removeCtx, runExecutionHandle(execRec))
	removeCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove detached container failed: %v", runID, err)
	}
	if err := s.store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s clear execution state failed: %v", runID, err)
	}
	if err := s.store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s clear run lease failed: %v", runID, err)
	}
}

func (s *server) stopRunExecutionBestEffort(runID string, note string) {
	execRec, ok := s.store.GetRunExecution(runID)
	if !ok {
		return
	}
	if _, err := s.store.UpdateRunExecutionState(runID, "stopping", execRec.ExitCode, time.Now().UTC()); err != nil {
		log.Printf("run %s mark execution stopping failed: %v", runID, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.launcher.Stop(stopCtx, runExecutionHandle(execRec), 10*time.Second)
	stopCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s stop execution failed (%s): %v", runID, note, err)
	}
}

func runExecutionHandle(execRec state.RunExecution) runner.ExecutionHandle {
	return runner.ExecutionHandle{
		Backend: strings.TrimSpace(execRec.Backend),
		ID:      strings.TrimSpace(execRec.ContainerID),
		Name:    strings.TrimSpace(execRec.ContainerName),
	}
}

func (s *server) startDetachedWithRetry(ctx context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	maxAttempts := s.cfg.RunnerMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var (
		handle runner.ExecutionHandle
		err    error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return handle, err
		}
		handle, err = s.launcher.StartDetached(ctx, spec)
		if err == nil {
			return handle, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return handle, context.Canceled
		}
		if attempt == maxAttempts {
			break
		}
		backoff := time.Duration(attempt) * time.Second
		log.Printf("run %s attempt %d/%d failed: %v (retrying in %s)", spec.RunID, attempt, maxAttempts, err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return handle, context.Canceled
		case <-timer.C:
		}
	}
	return handle, err
}

func (s *server) stopRunSupervisors() {
	s.mu.Lock()
	cancels := make([]context.CancelFunc, 0, len(s.runCancels))
	for _, cancel := range s.runCancels {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
}

func (s *server) setRunStatusWithFallback(run state.Run, status state.RunStatus, errText string) state.Run {
	updated, err := s.store.SetRunStatus(run.ID, status, errText)
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

func (s *server) setRunStatusBestEffort(runID string, status state.RunStatus, errText string) {
	if _, err := s.store.SetRunStatus(runID, status, errText); err != nil {
		log.Printf("run %s set status %q failed: %v", runID, status, err)
	}
}

func (s *server) clearRunCancelBestEffort(runID string) {
	if err := s.store.ClearRunCancel(runID); err != nil {
		log.Printf("run %s clear cancel request failed: %v", runID, err)
	}
}

func (s *server) deleteRunLeaseBestEffort(runID string) {
	if err := s.store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s delete run lease failed: %v", runID, err)
	}
}

func (s *server) deleteRunExecutionBestEffort(runID string) {
	if err := s.store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s delete run execution failed: %v", runID, err)
	}
}

func (s *server) releaseDeliveryClaimBestEffort(claim state.DeliveryClaim) {
	if err := s.store.ReleaseDeliveryClaim(claim); err != nil {
		log.Printf("release delivery claim %s failed: %v", claim.ID, err)
	}
}

func (s *server) setTaskPRBestEffort(taskID, repo string, prNumber int) {
	if err := s.store.SetTaskPR(taskID, repo, prNumber); err != nil {
		log.Printf("task %s set PR #%d failed: %v", taskID, prNumber, err)
	}
}

func (s *server) cancelQueuedRunsBestEffort(taskID, reason string) {
	if err := s.store.CancelQueuedRuns(taskID, reason); err != nil {
		log.Printf("task %s cancel queued runs failed: %v", taskID, err)
	}
}

func (s *server) updateRunBestEffort(runID string, fn func(*state.Run) error) {
	if _, err := s.store.UpdateRun(runID, fn); err != nil {
		log.Printf("run %s update failed: %v", runID, err)
	}
}

func (s *server) requestRunCancelBestEffort(runID, reason, source string) {
	if err := s.store.RequestRunCancel(runID, reason, source); err != nil {
		log.Printf("run %s request cancel failed: %v", runID, err)
	}
}

func (s *server) removeRunExecutionBestEffort(ctx context.Context, handle runner.ExecutionHandle, runID, note string) {
	err := s.launcher.Remove(ctx, handle)
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove execution failed (%s): %v", runID, note, err)
	}
}

func (s *server) finishRun(run state.Run) {
	if runStatusIsDone(run.Status) {
		s.clearRunCancelBestEffort(run.ID)
	}
	taskCompleted := s.store.IsTaskCompleted(run.TaskID)

	if taskCompleted {
		s.cancelQueuedRunsBestEffort(run.TaskID, "task completed; canceled pending runs")
	}

	if !s.isDraining() {
		s.scheduleRuns(run.TaskID)
	}
}

func (s *server) scheduleRuns(preferredTaskID string) {
	if s.isDraining() {
		return
	}
	preferredTaskID = strings.TrimSpace(preferredTaskID)

	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()

	for {
		atCapacity := s.activeRunCount() >= s.concurrencyLimit()
		draining := s.isDraining()
		if draining || atCapacity {
			return
		}

		run, claimed, err := s.store.ClaimNextQueuedRun(preferredTaskID)
		preferredTaskID = ""
		if err != nil {
			log.Printf("failed to claim next queued run: %v", err)
			return
		}
		if !claimed {
			return
		}

		if reason, ok := s.pendingRunCancelReason(run.ID); ok {
			s.setRunStatusBestEffort(run.ID, state.StatusCanceled, reason)
			s.clearRunCancelBestEffort(run.ID)
			continue
		}

		if s.isDraining() {
			s.setRunStatusBestEffort(run.ID, state.StatusCanceled, "orchestrator shutting down")
			return
		}
		if err := s.store.UpsertRunLease(run.ID, s.instanceID, runLeaseTTL); err != nil {
			s.setRunStatusBestEffort(run.ID, state.StatusFailed, fmt.Sprintf("claim run lease: %v", err))
			continue
		}

		go s.executeRun(run.ID)
	}
}

func (s *server) reconcileClosedPRRuns(repo string, prNumber int, merged bool) {
	repo = strings.TrimSpace(repo)
	if repo == "" || prNumber <= 0 {
		return
	}
	runs := s.store.ListRuns(10000)
	for _, run := range runs {
		if run.Repo != repo || run.PRNumber != prNumber {
			continue
		}
		s.updateRunBestEffort(run.ID, func(r *state.Run) error {
			now := time.Now().UTC()
			if merged {
				r.PRStatus = state.PRStatusMerged
				if r.Status == state.StatusReview {
					r.Status = state.StatusSucceeded
					r.Error = ""
					r.CompletedAt = &now
				}
				return nil
			}
			if r.PRStatus == state.PRStatusMerged {
				return nil
			}
			r.PRStatus = state.PRStatusClosedUnmerged
			if r.Status == state.StatusReview {
				r.Status = state.StatusCanceled
				r.Error = "pull request closed without merge"
				r.CompletedAt = &now
			}
			return nil
		})
	}
}

func (s *server) reconcileReopenedPRRuns(repo string, prNumber int) {
	repo = strings.TrimSpace(repo)
	if repo == "" || prNumber <= 0 {
		return
	}
	runs := s.store.ListRuns(10000)
	for _, run := range runs {
		if run.Repo != repo || run.PRNumber != prNumber {
			continue
		}
		s.updateRunBestEffort(run.ID, func(r *state.Run) error {
			if r.PRStatus == state.PRStatusMerged {
				return nil
			}
			r.PRStatus = state.PRStatusOpen
			return nil
		})
	}
}

func (s *server) taskForPR(repo string, prNumber int) (state.Task, bool) {
	if strings.TrimSpace(repo) == "" || prNumber <= 0 {
		return state.Task{}, false
	}
	return s.store.FindTaskByPR(repo, prNumber)
}

func (s *server) activeTaskForPR(repo string, prNumber int) (state.Task, bool) {
	task, ok := s.taskForPR(repo, prNumber)
	if !ok || task.Status != state.TaskOpen {
		return state.Task{}, false
	}
	return task, true
}

func (s *server) defaultBaseBranchForTask(taskID string) string {
	if run, ok := s.store.LastRunForTask(taskID); ok && run.BaseBranch != "" {
		return run.BaseBranch
	}
	return "main"
}

func (s *server) defaultHeadBranchForTask(taskID string) string {
	if run, ok := s.store.LastRunForTask(taskID); ok && run.HeadBranch != "" {
		return run.HeadBranch
	}
	return ""
}

func issueHasLabel(labels []ghapi.Label, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), name) {
			return true
		}
	}
	return false
}

func issueCommentBodyChanged(ev ghapi.IssueCommentEvent) bool {
	if ev.Changes.Body == nil {
		return false
	}
	newBody := strings.TrimSpace(ev.Comment.Body)
	oldBody := strings.TrimSpace(ev.Changes.Body.From)
	return newBody != oldBody
}

func reviewCommentBodyChanged(ev ghapi.PullRequestReviewCommentEvent) bool {
	if ev.Changes.Body == nil {
		return false
	}
	newBody := strings.TrimSpace(ev.Comment.Body)
	oldBody := strings.TrimSpace(ev.Changes.Body.From)
	return newBody != oldBody
}

func (s *server) isBotActor(login string) bool {
	login = strings.TrimSpace(strings.ToLower(login))
	if login == "" {
		return false
	}
	if strings.TrimSpace(s.cfg.BotLogin) != "" && login == strings.ToLower(strings.TrimSpace(s.cfg.BotLogin)) {
		return true
	}
	return strings.Contains(login, "[bot]")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON response failed: %v", err)
	}
}

func logRequests(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		reqID := requestIDFromContext(r.Context())
		if reqID != "" {
			log.Printf("%s %s (%s) request_id=%s", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond), reqID)
			return
		}
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}

func withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reqID := newRequestID()
		w.Header().Set("X-Request-ID", reqID)
		ctx := context.WithValue(r.Context(), requestIDKey{}, reqID)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

func requestIDFromContext(ctx context.Context) string {
	v := ctx.Value(requestIDKey{})
	if id, ok := v.(string); ok {
		return id
	}
	return ""
}

func newRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("req_%d", time.Now().UnixNano())
	}
	return "req_" + hex.EncodeToString(b)
}

func issueTaskFromIssue(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if body == "" {
		return title
	}
	return fmt.Sprintf(`%s

%s`, title, body)
}

func formatReviewCommentLocation(path string, startLine, line *int) string {
	path = strings.TrimSpace(path)
	if line != nil && *line > 0 {
		if startLine != nil && *startLine > 0 && *startLine != *line {
			if path == "" {
				return fmt.Sprintf("lines %d-%d", *startLine, *line)
			}
			return fmt.Sprintf("%s:%d-%d", path, *startLine, *line)
		}
		if path == "" {
			return fmt.Sprintf("line %d", *line)
		}
		return fmt.Sprintf("%s:%d", path, *line)
	}
	return path
}

func instructionText(run state.Run) string {
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
	b.WriteString(run.Task)
	b.WriteString(`

## Constraints

- Do not ask for interactive input.
- Do not require MCP tools.
- Keep changes minimal and scoped to the requested task.
- Run tests or explain why tests could not run.
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

func buildHeadBranch(taskID, task, runID string) string {
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

func defaultMaxConcurrent() int {
	n := runtime.NumCPU()
	if n < 1 {
		return 1
	}
	return n
}

func (s *server) concurrencyLimit() int {
	if s.maxConcurrent > 0 {
		return s.maxConcurrent
	}
	return 1
}

func (s *server) isDraining() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.draining
}

func (s *server) beginDrain() {
	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return
	}
	s.draining = true
	s.mu.Unlock()
}

func (s *server) waitForNoActiveRuns(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		active := s.activeRunCount()
		if active == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for active runs to finish")
}

func (s *server) activeRunCount() int {
	return s.store.CountRunLeasesByOwner(s.instanceID)
}

func (s *server) cancelRunningTaskRuns(taskID, reason string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.store.ListRunningRuns() {
		if run.TaskID != taskID {
			continue
		}
		if err := s.store.RequestRunCancel(run.ID, reason, "issue"); err != nil {
			log.Printf("failed to request run cancel for %s: %v", run.ID, err)
			continue
		}
		s.stopRunExecutionBestEffort(run.ID, "task cancellation")
	}
}

func (s *server) cancelActiveRuns(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.store.ListRunningRuns() {
		s.requestRunCancelBestEffort(run.ID, reason, "shutdown")
		s.stopRunExecutionBestEffort(run.ID, "shutdown cancellation")
	}
}

func (s *server) isActiveWebhookSlot() bool {
	slot := strings.TrimSpace(s.cfg.Slot)
	if slot == "" {
		return true
	}
	activePath := strings.TrimSpace(s.cfg.ActiveSlotPath)
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

func (s *server) pendingRunCancelReason(runID string) (string, bool) {
	req, ok := s.store.GetRunCancel(runID)
	if !ok {
		return "", false
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		reason = "canceled"
	}
	return reason, true
}

func isCommentTriggeredRun(trigger string) bool {
	switch strings.TrimSpace(trigger) {
	case "pr_comment", "pr_review", "pr_review_comment":
		return true
	default:
		return false
	}
}

func (s *server) cleanupAgentSessionsBestEffort() {
	ttlDays := s.cfg.EffectiveAgentSessionTTLDays()
	if ttlDays <= 0 {
		return
	}
	root := strings.TrimSpace(s.cfg.EffectiveAgentSessionRoot())
	if root == "" {
		root = filepath.Join(s.cfg.DataDir, defaults.AgentSessionDirName)
	}
	removed, err := cleanupStaleAgentSessionDirs(root, ttlDays, time.Now().UTC())
	if err != nil {
		log.Printf("agent session cleanup warning: root=%s ttl_days=%d error=%v", root, ttlDays, err)
		return
	}
	if removed > 0 {
		log.Printf("agent session cleanup: root=%s ttl_days=%d removed=%d", root, ttlDays, removed)
	}
}

func cleanupStaleAgentSessionDirs(root string, ttlDays int, now time.Time) (int, error) {
	if ttlDays <= 0 {
		return 0, nil
	}
	root = strings.TrimSpace(root)
	if root == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	cutoff := now.AddDate(0, 0, -ttlDays)
	removed := 0
	var firstErr error
	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		info, infoErr := entry.Info()
		if infoErr != nil {
			if firstErr == nil {
				firstErr = infoErr
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if rmErr := os.RemoveAll(path); rmErr != nil {
			if firstErr == nil {
				firstErr = rmErr
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func cleanupStaleGooseSessionDirs(root string, ttlDays int, now time.Time) (int, error) {
	return cleanupStaleAgentSessionDirs(root, ttlDays, now)
}

func resolveRunAgentLogPath(runDir string) (string, error) {
	primary := filepath.Join(strings.TrimSpace(runDir), agentLogFile)
	if info, err := os.Stat(primary); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("agent log path is a directory: %s", primary)
		}
		return primary, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	legacy := filepath.Join(strings.TrimSpace(runDir), legacyAgentLogFile)
	if info, err := os.Stat(legacy); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("legacy agent log path is a directory: %s", legacy)
		}
		return legacy, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}

	return primary, os.ErrNotExist
}

func tailRunAgentLog(runDir string, lines int) ([]string, string) {
	path, err := resolveRunAgentLogPath(runDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "(" + agentLogFile + " not found)"
		}
		return nil, "(" + agentLogFile + " unavailable)"
	}

	agentLines, err := logs.Tail(path, lines)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, "(" + agentLogFile + " not found)"
		}
		return nil, "(" + agentLogFile + " unavailable)"
	}
	return agentLines, ""
}

func loadRunResponseTarget(runDir string) (runResponseTarget, bool, error) {
	path := filepath.Join(strings.TrimSpace(runDir), runResponseTargetFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return runResponseTarget{}, false, nil
		}
		return runResponseTarget{}, false, fmt.Errorf("read run response target: %w", err)
	}
	var target runResponseTarget
	if err := json.Unmarshal(data, &target); err != nil {
		return runResponseTarget{}, false, fmt.Errorf("decode run response target: %w", err)
	}
	target.Repo = strings.TrimSpace(target.Repo)
	target.RequestedBy = strings.TrimSpace(target.RequestedBy)
	target.Trigger = strings.TrimSpace(target.Trigger)
	return target, true, nil
}

func runCompletionCommentMarkerPath(runDir string) string {
	return filepath.Join(strings.TrimSpace(runDir), runCompletionCommentMarkerFile)
}

func runCompletionCommentMarkerExists(runDir string) (bool, error) {
	path := runCompletionCommentMarkerPath(runDir)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat completion comment marker: %w", err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("completion comment marker path is a directory: %s", path)
	}
	return true, nil
}

func writeRunCompletionCommentMarker(run state.Run, repo string, issueNumber int) error {
	marker := runCompletionCommentMarker{
		RunID:       run.ID,
		Repo:        strings.TrimSpace(repo),
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encode completion comment marker: %w", err)
	}
	path := runCompletionCommentMarkerPath(run.RunDir)
	if err := writeFileAtomically(path, data, 0o644); err != nil {
		return fmt.Errorf("write completion comment marker: %w", err)
	}
	return nil
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := tempFile.Name()
	removeTemp := true
	defer func() {
		if removeTemp {
			if err := os.Remove(tempPath); err != nil && !errors.Is(err, os.ErrNotExist) {
				log.Printf("remove temp file %s: %v", tempPath, err)
			}
		}
	}()
	if _, err := tempFile.Write(data); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return fmt.Errorf("write temp file: %w (close temp file: %v)", err, closeErr)
		}
		return err
	}
	if err := tempFile.Chmod(mode); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return fmt.Errorf("chmod temp file: %w (close temp file: %v)", err, closeErr)
		}
		return err
	}
	if err := tempFile.Close(); err != nil {
		return err
	}
	if err := os.Rename(tempPath, path); err != nil {
		return err
	}
	removeTemp = false
	return nil
}

func (s *server) postRunCompletionCommentBestEffort(run state.Run) {
	if !isCommentTriggeredRun(run.Trigger) {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}

	target, ok, err := loadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
		return
	}
	if !ok {
		return
	}
	if markerExists, err := runCompletionCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check completion comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}
	// TODO: This per-run JSON marker deduplicates within a shared run directory.
	// Revisit a SQLite-backed guard if we need cross-instance/global dedupe guarantees.

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

	body, err := buildRunCompletionComment(run, target, repo)
	if err != nil {
		log.Printf("failed to build completion comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.gh.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post completion comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunCompletionCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist completion comment marker for run %s: %v", run.ID, err)
	}
}

func buildRunCompletionComment(run state.Run, target runResponseTarget, repo string) (string, error) {
	agentOutput := "(no agent output captured)"
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err == nil {
		if data, readErr := os.ReadFile(agentPath); readErr == nil {
			if strings.TrimSpace(string(data)) != "" {
				agentOutput = string(data)
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read agent log: %w", readErr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve agent log: %w", err)
	}

	commitMessageData, err := os.ReadFile(filepath.Join(run.RunDir, "commit_message.txt"))
	if err != nil && !os.IsNotExist(err) {
		return "", fmt.Errorf("read commit message: %w", err)
	}
	body, err := runsummary.BuildCompletionComment(runsummary.CompletionCommentInput{
		RunID:           run.ID,
		Repo:            repo,
		RequestedBy:     target.RequestedBy,
		HeadSHA:         run.HeadSHA,
		IssueNumber:     run.IssueNumber,
		GooseOutput:     agentOutput,
		CommitMessage:   commitMessageData,
		DurationSeconds: runsummary.RunDurationSeconds(run.CreatedAt, run.StartedAt, run.CompletedAt),
	})
	if err != nil {
		return "", err
	}
	return runCompletionCommentBodyMarker + "\n\n" + body, nil
}

func isRascalAutomationComment(body string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	if strings.Contains(trimmed, runCompletionCommentBodyMarker) {
		return true
	}
	legacy := strings.ToLower(trimmed)
	return strings.Contains(legacy, "rascal run `") && strings.Contains(legacy, "completed in ")
}

func (s *server) requeueRun(runID string) error {
	_, err := s.store.UpdateRun(runID, func(r *state.Run) error {
		if r.Status != state.StatusRunning {
			return nil
		}
		r.Status = state.StatusQueued
		r.Error = ""
		r.StartedAt = nil
		r.CompletedAt = nil
		return nil
	})
	return err
}

func (s *server) addIssueReactionBestEffort(repo string, issueNumber int, reaction string) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.gh.AddIssueReaction(ctx, repo, issueNumber, reaction); err != nil {
		log.Printf("failed to add %q reaction for %s#%d: %v", reaction, repo, issueNumber, err)
	}
}

func (s *server) removeIssueReactionsBestEffort(repo string, issueNumber int) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.gh.RemoveIssueReactions(ctx, repo, issueNumber); err != nil {
		log.Printf("failed to remove reactions for %s#%d: %v", repo, issueNumber, err)
	}
}

func (s *server) addIssueCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.gh.AddIssueCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for issue comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func (s *server) addPullRequestReviewReactionBestEffort(repo string, pullNumber int, reviewID int64, reaction string) {
	if reviewID <= 0 || pullNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.gh.AddPullRequestReviewReaction(ctx, repo, pullNumber, reviewID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review %d on %s#%d: %v", reaction, reviewID, repo, pullNumber, err)
	}
}

func (s *server) addPullRequestReviewCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.cfg.GitHubToken) == "" || s.gh == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.gh.AddPullRequestReviewCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func copyFile(src, dst string, mode os.FileMode) (err error) {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := in.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := out.Close(); err == nil && closeErr != nil {
			err = closeErr
		}
	}()

	if _, err = io.Copy(out, in); err != nil {
		return err
	}
	return nil
}
