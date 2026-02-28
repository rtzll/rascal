package main

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/christianrotzoll/rascal/internal/config"
	"github.com/christianrotzoll/rascal/internal/logs"
	"github.com/christianrotzoll/rascal/internal/state"
)

type server struct {
	cfg   config.ServerConfig
	store *state.Store
}

type createTaskRequest struct {
	Repo       string `json:"repo"`
	Task       string `json:"task"`
	BaseBranch string `json:"base_branch"`
}

type createIssueTaskRequest struct {
	Repo        string `json:"repo"`
	IssueNumber int    `json:"issue_number"`
}

func main() {
	cfg := config.LoadServerConfig()
	if err := cfg.Ensure(); err != nil {
		log.Fatalf("config: %v", err)
	}

	store, err := state.New(cfg.StatePath, cfg.MaxRuns)
	if err != nil {
		log.Fatalf("state: %v", err)
	}

	s := &server{cfg: cfg, store: store}
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/v1/runs", s.withAuth(s.handleListRuns))
	mux.HandleFunc("/v1/runs/", s.withAuth(s.handleRunSubresources))
	mux.HandleFunc("/v1/tasks", s.withAuth(s.handleCreateTask))
	mux.HandleFunc("/v1/tasks/issue", s.withAuth(s.handleCreateIssueTask))
	mux.HandleFunc("/v1/webhooks/github", s.handleWebhook)

	httpServer := &http.Server{
		Addr:              cfg.ListenAddr,
		Handler:           logRequests(mux),
		ReadHeaderTimeout: 5 * time.Second,
	}

	log.Printf("rascald listening on %s", cfg.ListenAddr)
	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server stopped: %v", err)
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
	req.Repo = strings.TrimSpace(req.Repo)
	req.Task = strings.TrimSpace(req.Task)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	if req.Repo == "" || req.Task == "" {
		http.Error(w, "repo and task are required", http.StatusBadRequest)
		return
	}
	if req.BaseBranch == "" {
		req.BaseBranch = "main"
	}

	runID, err := state.NewRunID()
	if err != nil {
		http.Error(w, "failed to create run id", http.StatusInternalServerError)
		return
	}
	taskID := runID
	runDir := filepath.Join(s.cfg.DataDir, "runs", runID)
	if err := os.MkdirAll(runDir, 0o755); err != nil {
		http.Error(w, "failed to create run dir", http.StatusInternalServerError)
		return
	}

	headBranch := fmt.Sprintf("rascal/%s", runID)
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         runID,
		TaskID:     taskID,
		Repo:       req.Repo,
		Task:       req.Task,
		BaseBranch: req.BaseBranch,
		HeadBranch: headBranch,
		Trigger:    "cli",
		RunDir:     runDir,
	})
	if err != nil {
		http.Error(w, "failed to persist run", http.StatusInternalServerError)
		return
	}

	_ = os.WriteFile(filepath.Join(runDir, "runner.log"), []byte(
		fmt.Sprintf("[%s] run created: %s\n", time.Now().Format(time.RFC3339), run.ID),
	), 0o644)

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
	if strings.TrimSpace(req.Repo) == "" || req.IssueNumber <= 0 {
		http.Error(w, "repo and issue_number are required", http.StatusBadRequest)
		return
	}

	http.Error(w, "not implemented yet", http.StatusNotImplemented)
}

func (s *server) handleWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	// Webhook verification + event processing will be added in milestone 3.
	writeJSON(w, http.StatusAccepted, map[string]any{"accepted": true})
}

func (s *server) handleRunSubresources(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/v1/runs/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "run id is required", http.StatusBadRequest)
		return
	}

	if strings.HasSuffix(path, "/logs") {
		runID := strings.TrimSuffix(path, "/logs")
		runID = strings.Trim(runID, "/")
		s.handleRunLogs(w, r, runID)
		return
	}
	s.handleGetRun(w, r, path)
}

func (s *server) handleGetRun(w http.ResponseWriter, _ *http.Request, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"run": run})
}

func (s *server) handleRunLogs(w http.ResponseWriter, _ *http.Request, runID string) {
	run, ok := s.store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}

	logPath := filepath.Join(run.RunDir, "runner.log")
	lines, err := logs.Tail(logPath, 200)
	if err != nil {
		if os.IsNotExist(err) {
			lines = nil
		} else {
			http.Error(w, "failed to read logs", http.StatusInternalServerError)
			return
		}
	}

	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	for _, line := range lines {
		_, _ = fmt.Fprintln(w, line)
	}
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
		log.Printf("%s %s (%s)", r.Method, r.URL.Path, time.Since(start).Round(time.Millisecond))
	})
}
