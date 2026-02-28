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
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/state"
)

var errTaskCompleted = errors.New("task is already completed")

type server struct {
	cfg      config.ServerConfig
	store    *state.Store
	launcher runner.Launcher
	gh       *ghapi.APIClient

	mu         sync.Mutex
	activeRuns map[string]string
	queuedRuns map[string][]string
	runCancels map[string]context.CancelFunc
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
}

type createTaskRequest struct {
	TaskID     string `json:"task_id"`
	Repo       string `json:"repo"`
	Task       string `json:"task"`
	BaseBranch string `json:"base_branch"`
}

type createIssueTaskRequest struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
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
		cfg:        cfg,
		store:      store,
		launcher:   runner.NewLauncher(cfg.RunnerMode, cfg.RunnerImage, cfg.GitHubToken),
		gh:         ghapi.NewAPIClient(cfg.GitHubToken),
		activeRuns: make(map[string]string),
		queuedRuns: make(map[string][]string),
		runCancels: make(map[string]context.CancelFunc),
	}
	s.recoverQueueState()

	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
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
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server stopped: %v", err)
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
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "service": "rascald"})
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
	})
	if err != nil {
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
	})
	if err != nil {
		if errors.Is(err, errTaskCompleted) {
			writeJSON(w, http.StatusConflict, map[string]any{"error": err.Error()})
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
		s.handleRunLogs(w, runID)
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

func (s *server) handleRunLogs(w http.ResponseWriter, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	runnerLines, err := logs.Tail(filepath.Join(run.RunDir, "runner.log"), 200)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read runner logs", http.StatusInternalServerError)
		return
	}
	gooseLines, err := logs.Tail(filepath.Join(run.RunDir, "goose.ndjson"), 200)
	if err != nil && !os.IsNotExist(err) {
		http.Error(w, "failed to read goose logs", http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = fmt.Fprintln(w, "== runner.log ==")
	for _, line := range runnerLines {
		_, _ = fmt.Fprintln(w, line)
	}
	_, _ = fmt.Fprintln(w, "\n== goose.ndjson ==")
	for _, line := range gooseLines {
		_, _ = fmt.Fprintln(w, line)
	}
}

func (s *server) createAndQueueRun(req runRequest) (state.Run, error) {
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
			req.HeadBranch = buildHeadBranch(req.TaskID, runID)
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
	if activeID, ok := s.activeRuns[run.TaskID]; ok && activeID != "" {
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
		updated, _ := s.store.SetRunStatus(run.ID, state.StatusCanceled, "task is already completed")
		s.finishRun(updated)
		return
	}

	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		log.Printf("failed to transition run to running: %s: %v", run.ID, err)
	}

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
		updated, _ := s.store.SetRunStatus(run.ID, status, errText)
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
		updated, _ = s.store.SetRunStatus(run.ID, state.StatusFailed, uErr.Error())
	}
	if updated.PRNumber > 0 {
		_ = s.store.SetTaskPR(updated.TaskID, updated.Repo, updated.PRNumber)
	}
	s.finishRun(updated)
}

func (s *server) finishRun(run state.Run) {
	taskCompleted := s.store.IsTaskCompleted(run.TaskID)
	var nextRunID string
	var remaining int

	s.mu.Lock()
	if current, ok := s.activeRuns[run.TaskID]; ok && current == run.ID {
		delete(s.activeRuns, run.TaskID)
	}

	queue := s.queuedRuns[run.TaskID]
	if taskCompleted {
		delete(s.queuedRuns, run.TaskID)
	} else if len(queue) > 0 {
		nextRunID = queue[0]
		queue = queue[1:]
		if len(queue) == 0 {
			delete(s.queuedRuns, run.TaskID)
		} else {
			s.queuedRuns[run.TaskID] = queue
		}
		remaining = len(queue)
		s.activeRuns[run.TaskID] = nextRunID
	} else {
		delete(s.queuedRuns, run.TaskID)
	}
	s.mu.Unlock()

	if taskCompleted {
		_ = s.store.CancelQueuedRuns(run.TaskID, "task completed; canceled pending runs")
		_ = s.store.SetTaskPendingInput(run.TaskID, false)
		return
	}

	_ = s.store.SetTaskPendingInput(run.TaskID, remaining > 0)
	if nextRunID != "" {
		go s.executeRun(nextRunID)
	}
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
	if strings.TrimSpace(run.Context) != "" {
		b.WriteString("\n## Additional Context\n\n")
		b.WriteString(run.Context)
		b.WriteString("\n")
	}
	return b.String()
}

func buildHeadBranch(taskID, runID string) string {
	taskID = strings.ToLower(strings.TrimSpace(taskID))
	var cleaned strings.Builder
	for _, r := range taskID {
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
	}
	return fmt.Sprintf("rascal/%s/%s", taskPart, runID)
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
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
