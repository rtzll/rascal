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

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

var errTaskCompleted = errors.New("task is already completed")
var errServerDraining = errors.New("orchestrator is draining")

type server struct {
	cfg      config.ServerConfig
	store    *state.Store
	launcher runner.Launcher
	gh       *ghapi.APIClient

	mu            sync.Mutex
	activeRuns    map[string]string
	queuedRuns    map[string][]string
	runCancels    map[string]context.CancelFunc
	maxConcurrent int
	draining      bool
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
	Context     string
	Debug       *bool
}

type createTaskRequest struct {
	TaskID     string `json:"task_id"`
	Repo       string `json:"repo"`
	Task       string `json:"task"`
	BaseBranch string `json:"base_branch"`
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
		launcher:      runner.NewLauncher(cfg.RunnerMode, cfg.RunnerImage, cfg.GitHubToken),
		gh:            ghapi.NewAPIClient(cfg.GitHubToken),
		activeRuns:    make(map[string]string),
		queuedRuns:    make(map[string][]string),
		runCancels:    make(map[string]context.CancelFunc),
		maxConcurrent: defaultMaxConcurrent(),
	}
	s.recoverQueueState()

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

	log.Printf("rascald listening on %s (runner=%s)", cfg.ListenAddr, cfg.RunnerMode)
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

	if err := s.waitForNoActiveRuns(5 * time.Minute); err != nil {
		log.Printf("active runs did not finish within drain timeout; canceling remaining runs")
		s.cancelActiveRuns()
		_ = s.waitForNoActiveRuns(30 * time.Second)
	}
}

func (s *server) recoverQueueState() {
	runs := s.store.ListRuns(10000)
	for i := len(runs) - 1; i >= 0; i-- {
		run := runs[i]
		switch run.Status {
		case state.StatusRunning:
			_, _ = s.store.SetRunStatus(run.ID, state.StatusFailed, "orchestrator restarted while run was active")
		case state.StatusQueued:
			s.enqueueExistingRun(run)
		}
	}
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

	limit := 50
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			http.Error(w, "invalid limit", http.StatusBadRequest)
			return
		}
		limit = parsed
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
	if req.Repo == "" || req.Task == "" {
		http.Error(w, "repo and task are required", http.StatusBadRequest)
		return
	}

	run, err := s.createAndQueueRun(runRequest{
		TaskID:     req.TaskID,
		Repo:       req.Repo,
		Task:       req.Task,
		BaseBranch: req.BaseBranch,
		Trigger:    "cli",
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
	if s.store.DeliverySeen(deliveryID) {
		writeJSON(w, http.StatusOK, map[string]any{"duplicate": true})
		return
	}

	eventType := ghapi.EventType(r.Header)
	if err := s.processWebhookEvent(r.Context(), eventType, payload); err != nil {
		http.Error(w, "webhook processing failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if err := s.store.RecordDelivery(deliveryID); err != nil {
		http.Error(w, "failed to persist delivery id", http.StatusInternalServerError)
		return
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
		if ev.Action != "labeled" || !strings.EqualFold(ev.Label.Name, "rascal") {
			return nil
		}
		if ev.Issue.PullRequest != nil {
			return nil
		}
		if s.isBotActor(ev.Sender.Login) {
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
	case "issue_comment":
		var ev ghapi.IssueCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return fmt.Errorf("decode issue_comment event: %w", err)
		}
		if ev.Action != "created" || ev.Issue.PullRequest == nil {
			return nil
		}
		if s.isBotActor(ev.Comment.User.Login) || s.isBotActor(ev.Sender.Login) {
			return nil
		}

		taskID := s.resolveTaskForPR(ev.Repository.FullName, ev.Issue.Number)
		_, err := s.createAndQueueRun(runRequest{
			TaskID:     taskID,
			Repo:       ev.Repository.FullName,
			Task:       fmt.Sprintf("Address PR #%d feedback", ev.Issue.Number),
			Trigger:    "pr_comment",
			PRNumber:   ev.Issue.Number,
			Context:    strings.TrimSpace(ev.Comment.Body),
			BaseBranch: s.defaultBaseBranchForTask(taskID),
			HeadBranch: s.defaultHeadBranchForTask(taskID),
			Debug:      boolPtr(true),
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

		taskID := s.resolveTaskForPR(ev.Repository.FullName, ev.PullRequest.Number)
		contextText := strings.TrimSpace(ev.Review.Body)
		if contextText == "" {
			contextText = fmt.Sprintf("review state: %s", ev.Review.State)
		}
		_, err := s.createAndQueueRun(runRequest{
			TaskID:     taskID,
			Repo:       ev.Repository.FullName,
			Task:       fmt.Sprintf("Address PR #%d review feedback", ev.PullRequest.Number),
			Trigger:    "pr_review",
			PRNumber:   ev.PullRequest.Number,
			Context:    contextText,
			BaseBranch: s.defaultBaseBranchForTask(taskID),
			HeadBranch: s.defaultHeadBranchForTask(taskID),
			Debug:      boolPtr(true),
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
		if ev.Action == "closed" && ev.PullRequest.Merged {
			taskID := s.resolveTaskForPR(ev.Repository.FullName, ev.PullRequest.Number)
			_, _ = s.store.UpsertTask(state.UpsertTaskInput{ID: taskID, Repo: ev.Repository.FullName, PRNumber: ev.PullRequest.Number})
			if err := s.store.MarkTaskCompleted(taskID); err != nil {
				return err
			}
			if err := s.store.CancelQueuedRuns(taskID, "task completed by merged PR"); err != nil {
				return err
			}
			_ = s.store.SetTaskPendingInput(taskID, false)
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
		writeJSON(w, http.StatusOK, map[string]any{"run": run, "canceled": false, "reason": "run already completed"})
		return
	}

	s.mu.Lock()
	if cancel, ok := s.runCancels[runID]; ok {
		cancel()
	}
	removed := s.removeQueuedRunLocked(run.TaskID, runID)
	s.mu.Unlock()

	if removed {
		updated, err := s.store.SetRunStatus(runID, state.StatusCanceled, "canceled by user")
		if err != nil {
			http.Error(w, "failed to cancel run", http.StatusInternalServerError)
			return
		}
		_ = s.store.SetTaskPendingInput(run.TaskID, s.taskHasQueuedRuns(run.TaskID))
		writeJSON(w, http.StatusOK, map[string]any{"run": updated, "canceled": true})
		return
	}

	// If the run is active, cancellation is cooperative via context.
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
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read runner logs", http.StatusInternalServerError)
		return
	}
	gooseLines, err := logs.Tail(filepath.Join(run.RunDir, "goose.ndjson"), lines)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read goose logs", http.StatusInternalServerError)
		return
	}

	var body strings.Builder
	_, _ = fmt.Fprintln(&body, "== runner.log ==")
	for _, line := range runnerLines {
		_, _ = fmt.Fprintln(&body, line)
	}
	_, _ = fmt.Fprintln(&body, "\n== goose.ndjson ==")
	for _, line := range gooseLines {
		_, _ = fmt.Fprintln(&body, line)
	}

	logsText := body.String()
	switch strings.ToLower(strings.TrimSpace(r.URL.Query().Get("format"))) {
	case "", "text":
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = io.WriteString(w, logsText)
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
	switch status {
	case state.StatusSucceeded, state.StatusFailed, state.StatusCanceled, state.StatusAwaitingFeedback:
		return true
	default:
		return false
	}
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
		ID:          req.TaskID,
		Repo:        req.Repo,
		IssueNumber: req.IssueNumber,
		PRNumber:    req.PRNumber,
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("upsert task: %w", err)
	}

	run, err := s.store.AddRun(state.CreateRunInput{
		ID:          runID,
		TaskID:      req.TaskID,
		Repo:        req.Repo,
		Task:        req.Task,
		BaseBranch:  req.BaseBranch,
		HeadBranch:  req.HeadBranch,
		Trigger:     req.Trigger,
		RunDir:      runDir,
		IssueNumber: req.IssueNumber,
		PRNumber:    req.PRNumber,
		Context:     req.Context,
		Debug:       boolPtr(debugEnabled),
	})
	if err != nil {
		return state.Run{}, fmt.Errorf("persist run: %w", err)
	}

	if err := s.writeRunFiles(run); err != nil {
		_, _ = s.store.SetRunStatus(run.ID, state.StatusFailed, err.Error())
		return state.Run{}, fmt.Errorf("prepare run files: %w", err)
	}

	s.enqueueExistingRun(run)
	return run, nil
}

func (s *server) writeRunFiles(run state.Run) error {
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
	defer f.Close()
	_, err = f.WriteString(logLine)
	return err
}

func (s *server) enqueueExistingRun(run state.Run) {
	startNow := false
	pending := false

	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		_, _ = s.store.SetRunStatus(run.ID, state.StatusCanceled, "orchestrator shutting down")
		_ = s.store.SetTaskPendingInput(run.TaskID, false)
		return
	}
	if activeID, ok := s.activeRuns[run.TaskID]; (ok && activeID != "") || len(s.queuedRuns[run.TaskID]) > 0 || len(s.activeRuns) >= s.concurrencyLimit() {
		s.queuedRuns[run.TaskID] = append(s.queuedRuns[run.TaskID], run.ID)
		pending = true
	} else {
		s.activeRuns[run.TaskID] = run.ID
		startNow = true
	}
	s.mu.Unlock()

	if pending {
		_ = s.store.SetTaskPendingInput(run.TaskID, true)
	}
	if startNow {
		_ = s.store.SetTaskPendingInput(run.TaskID, false)
		go s.executeRun(run.ID)
	}
}

func (s *server) executeRun(runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		return
	}

	if s.store.IsTaskCompleted(run.TaskID) {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, "task is already completed")
		s.finishRun(updated)
		return
	}

	run = s.setRunStatusWithFallback(run, state.StatusRunning, "")
	s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionEyes)

	spec := runner.Spec{
		RunID:       run.ID,
		TaskID:      run.TaskID,
		Repo:        run.Repo,
		Task:        run.Task,
		BaseBranch:  run.BaseBranch,
		HeadBranch:  run.HeadBranch,
		Trigger:     run.Trigger,
		RunDir:      run.RunDir,
		IssueNumber: run.IssueNumber,
		PRNumber:    run.PRNumber,
		Context:     run.Context,
		Debug:       run.Debug,
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	s.runCancels[runID] = cancel
	s.mu.Unlock()

	result, err := s.runLauncherWithRetry(ctx, spec)

	s.mu.Lock()
	delete(s.runCancels, runID)
	s.mu.Unlock()

	if err != nil {
		status := state.StatusFailed
		errText := err.Error()
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			status = state.StatusCanceled
			errText = "canceled by user"
		}
		updated := s.setRunStatusWithFallback(run, status, errText)
		if updated.Status == state.StatusFailed {
			s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionConfused)
		}
		s.finishRun(updated)
		return
	}

	now := time.Now().UTC()
	status := state.StatusSucceeded
	if result.PRNumber > 0 || strings.TrimSpace(result.PRURL) != "" {
		status = state.StatusAwaitingFeedback
	}
	updated, uErr := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.Status = status
		r.Error = ""
		r.PRNumber = maxInt(r.PRNumber, result.PRNumber)
		if strings.TrimSpace(result.PRURL) != "" {
			r.PRURL = strings.TrimSpace(result.PRURL)
		}
		if strings.TrimSpace(result.HeadSHA) != "" {
			r.HeadSHA = strings.TrimSpace(result.HeadSHA)
		}
		r.CompletedAt = &now
		return nil
	})
	if uErr != nil {
		log.Printf("failed to persist run result for %s: %v", run.ID, uErr)
		updated = s.setRunStatusWithFallback(run, state.StatusFailed, uErr.Error())
	}
	switch updated.Status {
	case state.StatusSucceeded, state.StatusAwaitingFeedback:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionRocket)
	case state.StatusFailed:
		s.addIssueReactionBestEffort(updated.Repo, updated.IssueNumber, ghapi.ReactionConfused)
	}
	if updated.PRNumber > 0 {
		_ = s.store.SetTaskPR(updated.TaskID, updated.Repo, updated.PRNumber)
	}
	s.finishRun(updated)
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
	if status == state.StatusSucceeded || status == state.StatusFailed || status == state.StatusCanceled || status == state.StatusAwaitingFeedback {
		run.CompletedAt = &now
	}
	return run
}

func (s *server) finishRun(run state.Run) {
	if s.isDraining() {
		_ = s.store.CancelQueuedRuns(run.TaskID, "orchestrator shutting down")
		_ = s.store.SetTaskPendingInput(run.TaskID, false)
	}

	taskCompleted := s.store.IsTaskCompleted(run.TaskID)
	var (
		nextRunID     string
		nextTaskID    string
		pendingByTask = make(map[string]bool)
	)

	s.mu.Lock()
	if current, ok := s.activeRuns[run.TaskID]; ok && current == run.ID {
		delete(s.activeRuns, run.TaskID)
	}
	if taskCompleted {
		delete(s.queuedRuns, run.TaskID)
		pendingByTask[run.TaskID] = false
	}

	preferredTaskID := ""
	if !taskCompleted {
		preferredTaskID = run.TaskID
	}
	if !s.draining && len(s.activeRuns) < s.concurrencyLimit() {
		taskID, runID, remaining, ok := s.popNextQueuedRunLocked(preferredTaskID)
		if ok {
			nextTaskID = taskID
			nextRunID = runID
			s.activeRuns[taskID] = runID
			pendingByTask[taskID] = remaining > 0
		}
	}
	if !taskCompleted {
		if _, ok := pendingByTask[run.TaskID]; !ok {
			pendingByTask[run.TaskID] = len(s.queuedRuns[run.TaskID]) > 0
		}
	}
	s.mu.Unlock()

	if taskCompleted {
		_ = s.store.CancelQueuedRuns(run.TaskID, "task completed; canceled pending runs")
	}

	for taskID, pending := range pendingByTask {
		_ = s.store.SetTaskPendingInput(taskID, pending)
	}

	if nextRunID != "" {
		if s.isDraining() {
			_, _ = s.store.SetRunStatus(nextRunID, state.StatusCanceled, "orchestrator shutting down")
			s.mu.Lock()
			if activeID, ok := s.activeRuns[nextTaskID]; ok && activeID == nextRunID {
				delete(s.activeRuns, nextTaskID)
			}
			s.mu.Unlock()
			_ = s.store.SetTaskPendingInput(nextTaskID, s.taskHasQueuedRuns(nextTaskID))
			return
		}
		go s.executeRun(nextRunID)
	}
}

func (s *server) popNextQueuedRunLocked(preferredTaskID string) (taskID, runID string, remaining int, ok bool) {
	if preferredTaskID != "" {
		if runID, remaining, ok = s.popQueuedRunForTaskLocked(preferredTaskID); ok {
			return preferredTaskID, runID, remaining, true
		}
	}
	for candidateTaskID := range s.queuedRuns {
		if candidateTaskID == preferredTaskID {
			continue
		}
		if runID, remaining, ok = s.popQueuedRunForTaskLocked(candidateTaskID); ok {
			return candidateTaskID, runID, remaining, true
		}
	}
	return "", "", 0, false
}

func (s *server) popQueuedRunForTaskLocked(taskID string) (runID string, remaining int, ok bool) {
	if _, busy := s.activeRuns[taskID]; busy {
		return "", 0, false
	}
	queue := s.queuedRuns[taskID]
	if len(queue) == 0 {
		delete(s.queuedRuns, taskID)
		return "", 0, false
	}
	runID = queue[0]
	queue = queue[1:]
	if len(queue) == 0 {
		delete(s.queuedRuns, taskID)
	} else {
		s.queuedRuns[taskID] = queue
	}
	return runID, len(queue), true
}

func (s *server) removeQueuedRunLocked(taskID, runID string) bool {
	queue := s.queuedRuns[taskID]
	if len(queue) == 0 {
		return false
	}
	out := queue[:0]
	removed := false
	for _, id := range queue {
		if id == runID {
			removed = true
			continue
		}
		out = append(out, id)
	}
	if len(out) == 0 {
		delete(s.queuedRuns, taskID)
	} else {
		s.queuedRuns[taskID] = out
	}
	return removed
}

func (s *server) taskHasQueuedRuns(taskID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.queuedRuns[taskID]) > 0
}

func (s *server) resolveTaskForPR(repo string, prNumber int) string {
	task, ok := s.store.FindTaskByPR(repo, prNumber)
	if ok {
		return task.ID
	}
	return fmt.Sprintf("%s#pr-%d", repo, prNumber)
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
	_ = json.NewEncoder(w).Encode(v)
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
	return fmt.Sprintf("%s\n\n%s", title, body)
}

func instructionText(run state.Run) string {
	var b strings.Builder
	b.WriteString("# Rascal Run Instructions\n\n")
	b.WriteString(fmt.Sprintf("Run ID: %s\n", run.ID))
	b.WriteString(fmt.Sprintf("Task ID: %s\n", run.TaskID))
	b.WriteString(fmt.Sprintf("Repository: %s\n", run.Repo))
	if run.IssueNumber > 0 {
		b.WriteString(fmt.Sprintf("Issue: #%d\n", run.IssueNumber))
	}
	if run.PRNumber > 0 {
		b.WriteString(fmt.Sprintf("Pull Request: #%d\n", run.PRNumber))
	}
	b.WriteString("\n## Task\n\n")
	b.WriteString(run.Task)
	b.WriteString("\n\n## Constraints\n\n")
	b.WriteString("- Do not ask for interactive input.\n")
	b.WriteString("- Do not require MCP tools.\n")
	b.WriteString("- Keep changes minimal and scoped to the requested task.\n")
	b.WriteString("- Run tests or explain why tests could not run.\n")
	b.WriteString("- If you make changes, write /rascal-meta/commit_message.txt using a conventional commit title on the first line.\n")
	b.WriteString("- Optionally add a commit body after a blank line in /rascal-meta/commit_message.txt.\n")
	if strings.TrimSpace(run.Context) != "" {
		b.WriteString("\n## Additional Context\n\n")
		b.WriteString(run.Context)
		b.WriteString("\n")
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
	queuedByTask := make(map[string][]string, len(s.queuedRuns))
	for taskID, ids := range s.queuedRuns {
		queuedByTask[taskID] = append([]string(nil), ids...)
	}
	s.queuedRuns = make(map[string][]string)
	s.mu.Unlock()

	for taskID, ids := range queuedByTask {
		for _, runID := range ids {
			_, _ = s.store.SetRunStatus(runID, state.StatusCanceled, "orchestrator shutting down")
		}
		_ = s.store.SetTaskPendingInput(taskID, false)
	}
}

func (s *server) waitForNoActiveRuns(timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		active := len(s.activeRuns)
		s.mu.Unlock()
		if active == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("timed out waiting for active runs to finish")
}

func (s *server) cancelActiveRuns() {
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

func (s *server) runLauncherWithRetry(ctx context.Context, spec runner.Spec) (runner.Result, error) {
	maxAttempts := s.cfg.RunnerMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}

	var (
		res runner.Result
		err error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return res, err
		}
		res, err = s.launcher.Start(ctx, spec)
		if err == nil {
			return res, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return res, context.Canceled
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
			return res, context.Canceled
		case <-timer.C:
		}
	}
	return res, err
}

func copyFile(src, dst string, mode os.FileMode) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, mode)
	if err != nil {
		return err
	}
	defer out.Close()

	if _, err := io.Copy(out, in); err != nil {
		return err
	}
	return out.Close()
}
