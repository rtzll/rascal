package orchestrator

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
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
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/defaults"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runsummary"
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

type requestIDKey struct{}
type authPrincipalKey struct{}

type authPrincipal struct {
	UserID        string
	ExternalLogin string
	Role          state.UserRole
}

func WithPrincipal(req *http.Request, userID, externalLogin string, role state.UserRole) *http.Request {
	ctx := context.WithValue(req.Context(), authPrincipalKey{}, authPrincipal{
		UserID:        strings.TrimSpace(userID),
		ExternalLogin: strings.TrimSpace(externalLogin),
		Role:          role,
	})
	return req.WithContext(ctx)
}

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

func (s *Server) Mount(mux *http.ServeMux) {
	mux.HandleFunc("/healthz", s.HandleHealth)
	mux.HandleFunc("/readyz", s.HandleReady)
	mux.HandleFunc("/v1/runs", s.WithAuth(s.HandleListRuns))
	mux.HandleFunc("/v1/runs/", s.WithAuth(s.HandleRunSubresources))
	mux.HandleFunc("/v1/tasks", s.WithAuth(s.HandleCreateTask))
	mux.HandleFunc("/v1/tasks/", s.WithAuth(s.HandleTaskSubresources))
	mux.HandleFunc("/v1/tasks/issue", s.WithAuth(s.HandleCreateIssueTask))
	mux.HandleFunc("/v1/credentials", s.WithAuth(s.HandleCredentials))
	mux.HandleFunc("/v1/credentials/", s.WithAuth(s.HandleCredentialSubresources))
	mux.HandleFunc("/v1/webhooks/github", s.HandleWebhook)
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

func (s *Server) RecoverRunningRuns() {
	now := time.Now().UTC()
	runs := s.Store.ListRunningRuns()
	for _, run := range runs {
		if exec, ok := s.Store.GetRunExecution(run.ID); ok {
			s.recoverDetachedRun(run, exec)
			continue
		}
		if reason, ok := s.pendingRunCancelReason(run.ID); ok {
			s.setRunStatusBestEffort(run.ID, state.StatusCanceled, reason)
			s.clearRunCancelBestEffort(run.ID)
			continue
		}

		lease, hasLease := s.Store.GetRunLease(run.ID)
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

func (s *Server) recoverDetachedRun(run state.Run, execRec state.RunExecution) {
	handle := runExecutionHandle(execRec)
	inspectCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	execState, err := s.Launcher.Inspect(inspectCtx, handle)
	switch {
	case errors.Is(err, runner.ErrExecutionNotFound):
		s.failRunForMissingExecution(run, "detached container missing during adoption")
		return
	case err != nil:
		log.Printf("recover run %s inspect failed, adopting with retry loop: %v", run.ID, err)
		if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec, s.activeCredentialLeaseIDForRun(run.ID))
		return
	}

	if execState.Running {
		if _, err := s.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusRunning, 0, time.Now().UTC()); err != nil {
			log.Printf("recover run %s update execution running state failed: %v", run.ID, err)
		}
		if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
			log.Printf("recover run %s claim run lease failed: %v", run.ID, err)
			return
		}
		go s.superviseDetachedRunLoop(run.ID, execRec, s.activeCredentialLeaseIDForRun(run.ID))
		return
	}

	exitCode := 0
	if execState.ExitCode != nil {
		exitCode = *execState.ExitCode
	}
	if _, err := s.Store.UpdateRunExecutionState(run.ID, state.RunExecutionStatusExited, exitCode, time.Now().UTC()); err != nil {
		log.Printf("recover run %s update execution exited state failed: %v", run.ID, err)
	}
	s.finalizeDetachedRun(run.ID, execRec, exitCode)
}

func (s *Server) failRunForMissingExecution(run state.Run, reason string) {
	updated := s.setRunStatusWithFallback(run, state.StatusFailed, reason)
	s.deleteRunExecutionBestEffort(run.ID)
	s.deleteRunLeaseBestEffort(run.ID)
	s.finishRun(updated)
}

func (s *Server) WithAuth(next http.HandlerFunc) http.HandlerFunc {
	if !s.Config.AuthEnabled() {
		return func(w http.ResponseWriter, r *http.Request) {
			ctx := context.WithValue(r.Context(), authPrincipalKey{}, authPrincipal{
				UserID:        "anonymous",
				ExternalLogin: "anonymous",
				Role:          state.UserRoleAdmin,
			})
			next(w, r.WithContext(ctx))
		}
	}

	return func(w http.ResponseWriter, r *http.Request) {
		auth := strings.TrimSpace(r.Header.Get("Authorization"))
		const bearer = "Bearer "
		if !strings.HasPrefix(auth, bearer) {
			http.Error(w, "missing bearer token", http.StatusUnauthorized)
			return
		}
		provided := strings.TrimPrefix(auth, bearer)
		if strings.TrimSpace(provided) == "" {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		if subtle.ConstantTimeCompare([]byte(provided), []byte(strings.TrimSpace(s.Config.APIToken))) == 1 {
			ctx := context.WithValue(r.Context(), authPrincipalKey{}, authPrincipal{
				UserID:        "bootstrap-admin",
				ExternalLogin: "bootstrap-admin",
				Role:          state.UserRoleAdmin,
			})
			next(w, r.WithContext(ctx))
			return
		}
		keyHash := hashAPIKey(provided)
		principal, ok, err := s.Store.ResolveAPIPrincipalByKeyHash(keyHash)
		if err != nil {
			http.Error(w, "auth lookup failed", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "invalid token", http.StatusUnauthorized)
			return
		}
		ctx := context.WithValue(r.Context(), authPrincipalKey{}, authPrincipal{
			UserID:        principal.UserID,
			ExternalLogin: principal.ExternalLogin,
			Role:          principal.Role,
		})
		next(w, r.WithContext(ctx))
	}
}

func (s *Server) BootstrapAuth() error {
	if _, err := s.Store.UpsertUser(state.UpsertUserInput{
		ID:            "system",
		ExternalLogin: "system",
		Role:          state.UserRoleAdmin,
	}); err != nil {
		return fmt.Errorf("bootstrap system user: %w", err)
	}
	token := strings.TrimSpace(s.Config.APIToken)
	if token == "" {
		return nil
	}
	if _, err := s.Store.UpsertUser(state.UpsertUserInput{
		ID:            "bootstrap-admin",
		ExternalLogin: "bootstrap-admin",
		Role:          state.UserRoleAdmin,
	}); err != nil {
		return fmt.Errorf("bootstrap admin user: %w", err)
	}
	if err := s.Store.UpsertAPIKey(state.UpsertAPIKeyInput{
		ID:      "bootstrap-admin-key",
		UserID:  "bootstrap-admin",
		KeyHash: hashAPIKey(token),
		Label:   "bootstrap",
	}); err != nil {
		return fmt.Errorf("bootstrap admin api key: %w", err)
	}
	return nil
}

func hashAPIKey(token string) string {
	sum := sha256.Sum256([]byte(strings.TrimSpace(token)))
	return hex.EncodeToString(sum[:])
}

func principalFromContext(ctx context.Context) authPrincipal {
	if v := ctx.Value(authPrincipalKey{}); v != nil {
		if principal, ok := v.(authPrincipal); ok {
			return principal
		}
	}
	return authPrincipal{UserID: "system", ExternalLogin: "system", Role: state.UserRoleAdmin}
}

func requesterUserID(ctx context.Context) string {
	userID := strings.TrimSpace(principalFromContext(ctx).UserID)
	if userID == "" {
		return "system"
	}
	return userID
}

func requesterIsAdmin(ctx context.Context) bool {
	return principalFromContext(ctx).Role == state.UserRoleAdmin
}

func (s *Server) HandleHealth(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	writeJSON(w, http.StatusOK, api.ServiceStatusResponse{OK: true, Service: "rascald", Ready: !s.isDraining()})
}

func (s *Server) HandleReady(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if s.isDraining() {
		writeJSON(w, http.StatusServiceUnavailable, api.ServiceStatusResponse{OK: false, Service: "rascald", Ready: false})
		return
	}
	writeJSON(w, http.StatusOK, api.ServiceStatusResponse{OK: true, Service: "rascald", Ready: true})
}

func (s *Server) HandleListRuns(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, api.RunsResponse{Runs: s.Store.ListRuns(limit)})
}

func (s *Server) HandleCreateTask(w http.ResponseWriter, r *http.Request) {
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
	req.Instruction = strings.TrimSpace(req.Instruction)
	req.BaseBranch = strings.TrimSpace(req.BaseBranch)
	if req.Repo == "" || req.Instruction == "" {
		http.Error(w, "repo and task are required", http.StatusBadRequest)
		return
	}
	trigger, err := runtrigger.ParseOrDefault(req.Trigger.String(), runtrigger.NameCLI)
	if err != nil {
		http.Error(w, "invalid trigger", http.StatusBadRequest)
		return
	}

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:          req.TaskID,
		Repo:            req.Repo,
		Instruction:     req.Instruction,
		BaseBranch:      req.BaseBranch,
		Trigger:         trigger,
		Debug:           req.Debug,
		CreatedByUserID: requesterUserID(r.Context()),
	})
	if err != nil {
		if errors.Is(err, errServerDraining) {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to create run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, api.RunResponse{Run: run})
}

func (s *Server) HandleCreateIssueTask(w http.ResponseWriter, r *http.Request) {
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
	req.Repo = state.NormalizeRepo(req.Repo)

	taskID := repoIssueTaskID(req.Repo, req.IssueNumber)
	taskText := fmt.Sprintf("Work on issue #%d in %s", req.IssueNumber, req.Repo)
	ctxText := ""
	requestedBy := requesterUserID(r.Context())
	if requestedBy == "system" {
		requestedBy = ""
	}
	if strings.TrimSpace(s.Config.GitHubToken) != "" {
		issue, err := s.GitHub.GetIssue(r.Context(), req.Repo, req.IssueNumber)
		if err != nil {
			http.Error(w, "failed to fetch issue: "+err.Error(), http.StatusBadGateway)
			return
		}
		taskText = ghapi.IssueTaskFromIssue(issue.Title, issue.Body)
		ctxText = fmt.Sprintf("Issue URL: %s", issue.HTMLURL)
	}

	run, err := s.CreateAndQueueRun(RunRequest{
		TaskID:          taskID,
		Repo:            req.Repo,
		Instruction:     taskText,
		Trigger:         runtrigger.NameIssueAPI,
		IssueNumber:     req.IssueNumber,
		Context:         ctxText,
		Debug:           req.Debug,
		CreatedByUserID: requesterUserID(r.Context()),
		ResponseTarget: &RunResponseTarget{
			Repo:        req.Repo,
			IssueNumber: req.IssueNumber,
			RequestedBy: requestedBy,
			Trigger:     runtrigger.NameIssueAPI,
		},
	})
	if err != nil {
		if errors.Is(err, errTaskCompleted) {
			writeJSON(w, http.StatusConflict, api.ErrorResponse{Error: err.Error()})
			return
		}
		if errors.Is(err, errServerDraining) {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		http.Error(w, "failed to create run: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusAccepted, api.RunResponse{Run: run})
}

func (s *Server) HandleCredentials(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		var (
			creds []state.CodexCredential
			err   error
		)
		if requesterIsAdmin(r.Context()) {
			creds, err = s.Store.ListAllCodexCredentials()
		} else {
			creds, err = s.Store.ListCodexCredentialsByOwner(requesterUserID(r.Context()))
		}
		if err != nil {
			http.Error(w, "failed to list credentials", http.StatusInternalServerError)
			return
		}
		out := make([]api.Credential, 0, len(creds))
		for _, credential := range creds {
			if !s.canAccessCredential(r.Context(), credential) {
				continue
			}
			out = append(out, credentialResponse(credential))
		}
		writeJSON(w, http.StatusOK, api.CredentialListResponse{Credentials: out})
	case http.MethodPost:
		var req createCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		if strings.TrimSpace(req.AuthBlob) == "" {
			http.Error(w, "auth_blob is required", http.StatusBadRequest)
			return
		}
		scope, ok := state.ParseCredentialScope(string(req.Scope))
		if !ok {
			http.Error(w, "invalid scope", http.StatusBadRequest)
			return
		}
		ownerUserID := strings.TrimSpace(req.OwnerUserID)
		if !requesterIsAdmin(r.Context()) {
			scope = state.CredentialScopePersonal
			ownerUserID = requesterUserID(r.Context())
		}
		if scope == state.CredentialScopeShared {
			ownerUserID = ""
		}
		if scope == state.CredentialScopePersonal && ownerUserID == "" {
			ownerUserID = requesterUserID(r.Context())
		}
		id := strings.TrimSpace(req.ID)
		if id == "" {
			var err error
			id, err = newCredentialID()
			if err != nil {
				http.Error(w, "failed to create credential id", http.StatusInternalServerError)
				return
			}
		}
		encrypted, err := s.Cipher.Encrypt([]byte(req.AuthBlob))
		if err != nil {
			http.Error(w, "failed to encrypt auth blob", http.StatusInternalServerError)
			return
		}
		credential, err := s.Store.CreateCodexCredential(state.CreateCodexCredentialInput{
			ID:                id,
			OwnerUserID:       ownerUserID,
			Scope:             scope,
			EncryptedAuthBlob: encrypted,
			Weight:            req.Weight,
			Status:            state.CredentialStatusActive,
		})
		if err != nil {
			http.Error(w, "failed to create credential: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("audit event=credential_created credential_id=%s scope=%s owner_user_id=%s actor_user_id=%s", credential.ID, credential.Scope, credential.OwnerUserID, requesterUserID(r.Context()))
		writeJSON(w, http.StatusCreated, api.CredentialResponse{Credential: credentialResponse(credential)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) HandleCredentialSubresources(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/credentials/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "credential id is required", http.StatusBadRequest)
		return
	}
	id, err := url.PathUnescape(path)
	if err != nil {
		http.Error(w, "invalid credential id", http.StatusBadRequest)
		return
	}
	credential, ok, err := s.Store.GetCodexCredential(id)
	if err != nil {
		http.Error(w, "failed to load credential", http.StatusInternalServerError)
		return
	}
	if !ok || !s.canAccessCredential(r.Context(), credential) {
		http.Error(w, "credential not found", http.StatusNotFound)
		return
	}

	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, api.CredentialResponse{Credential: credentialResponse(credential)})
	case http.MethodDelete:
		if err := s.Store.SetCodexCredentialStatus(credential.ID, state.CredentialStatusDisabled, nil, "disabled by API"); err != nil {
			http.Error(w, "failed to disable credential", http.StatusInternalServerError)
			return
		}
		log.Printf("audit event=credential_disabled credential_id=%s actor_user_id=%s", credential.ID, requesterUserID(r.Context()))
		writeJSON(w, http.StatusOK, api.CredentialDisabledResponse{Disabled: true})
	case http.MethodPatch:
		var req updateCredentialRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		updated := state.UpdateCodexCredentialInput{
			ID:                credential.ID,
			OwnerUserID:       credential.OwnerUserID,
			Scope:             credential.Scope,
			EncryptedAuthBlob: credential.EncryptedAuthBlob,
			Weight:            credential.Weight,
			Status:            credential.Status,
			CooldownUntil:     credential.CooldownUntil,
			LastError:         credential.LastError,
		}
		if req.OwnerUserID != nil {
			if !requesterIsAdmin(r.Context()) {
				http.Error(w, "owner changes require admin", http.StatusForbidden)
				return
			}
			updated.OwnerUserID = strings.TrimSpace(*req.OwnerUserID)
		}
		if req.Scope != nil {
			scope, ok := state.ParseCredentialScope(string(*req.Scope))
			if !ok {
				http.Error(w, "invalid scope", http.StatusBadRequest)
				return
			}
			if !requesterIsAdmin(r.Context()) && scope == state.CredentialScopeShared {
				http.Error(w, "shared scope requires admin", http.StatusForbidden)
				return
			}
			updated.Scope = scope
			if scope == state.CredentialScopeShared {
				updated.OwnerUserID = ""
			}
		}
		if req.AuthBlob != nil {
			encrypted, err := s.Cipher.Encrypt([]byte(strings.TrimSpace(*req.AuthBlob)))
			if err != nil {
				http.Error(w, "failed to encrypt auth blob", http.StatusInternalServerError)
				return
			}
			updated.EncryptedAuthBlob = encrypted
		}
		if req.Weight != nil {
			updated.Weight = *req.Weight
		}
		if req.Status != nil {
			status, ok := state.ParseCredentialStatus(string(*req.Status))
			if !ok {
				http.Error(w, "invalid status", http.StatusBadRequest)
				return
			}
			updated.Status = status
		}
		if req.CooldownUntil != nil {
			value := strings.TrimSpace(*req.CooldownUntil)
			if value == "" {
				updated.CooldownUntil = nil
			} else {
				parsed, err := time.Parse(time.RFC3339, value)
				if err != nil {
					http.Error(w, "invalid cooldown_until", http.StatusBadRequest)
					return
				}
				parsed = parsed.UTC()
				updated.CooldownUntil = &parsed
			}
		}
		if req.LastError != nil {
			updated.LastError = strings.TrimSpace(*req.LastError)
		}
		credential, err := s.Store.UpdateCodexCredential(updated)
		if err != nil {
			http.Error(w, "failed to update credential: "+err.Error(), http.StatusInternalServerError)
			return
		}
		log.Printf("audit event=credential_updated credential_id=%s actor_user_id=%s", credential.ID, requesterUserID(r.Context()))
		writeJSON(w, http.StatusOK, api.CredentialResponse{Credential: credentialResponse(credential)})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) canAccessCredential(ctx context.Context, credential state.CodexCredential) bool {
	if requesterIsAdmin(ctx) {
		return true
	}
	return credential.Scope == state.CredentialScopePersonal && credential.OwnerUserID == requesterUserID(ctx)
}

func credentialResponse(credential state.CodexCredential) api.Credential {
	return api.CredentialFromState(credential)
}

func newCredentialID() (string, error) {
	buf := make([]byte, 8)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate credential id: %w", err)
	}
	return "cred_" + hex.EncodeToString(buf), nil
}

func (s *Server) HandleTaskSubresources(w http.ResponseWriter, r *http.Request) {
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
	task, ok := s.Store.GetTask(taskID)
	if !ok {
		http.Error(w, "task not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, api.TaskResponse{Task: task})
}

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

func (s *Server) HandleRunSubresources(w http.ResponseWriter, r *http.Request) {
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
		s.HandleCancelRun(w, runID)
		return
	default:
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		s.HandleGetRun(w, path)
		return
	}
}

func (s *Server) HandleGetRun(w http.ResponseWriter, runID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	writeJSON(w, http.StatusOK, api.RunResponse{Run: run})
}

func (s *Server) HandleCancelRun(w http.ResponseWriter, runID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok {
		http.Error(w, "run not found", http.StatusNotFound)
		return
	}
	if run.Status == state.StatusSucceeded || run.Status == state.StatusFailed || run.Status == state.StatusCanceled {
		s.clearRunCancelBestEffort(runID)
		canceled := false
		writeJSON(w, http.StatusOK, api.RunCancelResponse{Run: &run, Canceled: &canceled, Reason: "run already completed"})
		return
	}
	if err := s.Store.RequestRunCancel(runID, "canceled by user", "user"); err != nil {
		http.Error(w, "failed to persist cancel request", http.StatusInternalServerError)
		return
	}

	if run.Status == state.StatusQueued {
		updated, err := s.Store.SetRunStatus(runID, state.StatusCanceled, "canceled by user")
		if err != nil {
			http.Error(w, "failed to cancel run", http.StatusInternalServerError)
			return
		}
		s.clearRunCancelBestEffort(runID)
		if !s.isDraining() {
			s.ScheduleRuns(run.TaskID)
		}
		canceled := true
		writeJSON(w, http.StatusOK, api.RunCancelResponse{Run: &updated, Canceled: &canceled})
		return
	}

	s.stopRunExecutionBestEffort(runID, "user cancel requested")
	cancelRequested := true
	writeJSON(w, http.StatusAccepted, api.RunCancelResponse{RunID: runID, CancelRequested: &cancelRequested})
}

func (s *Server) handleRunLogs(w http.ResponseWriter, r *http.Request, runID string) {
	run, ok := s.Store.GetRun(runID)
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
		writeJSON(w, http.StatusOK, api.RunLogsResponse{
			Logs:      logsText,
			RunStatus: run.Status,
			Done:      runStatusIsDone(run.Status),
		})
	default:
		http.Error(w, "invalid format", http.StatusBadRequest)
	}
}

func runStatusIsDone(status state.RunStatus) bool {
	return state.IsFinalRunStatus(status)
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

func (s *Server) prepareRunCredentialAuth(runID, runDir, requesterUserID string) (string, error) {
	requesterUserID = strings.TrimSpace(requesterUserID)
	if requesterUserID == "" {
		requesterUserID = "system"
	}
	authDir := filepath.Join(runDir, "codex")
	authPath := filepath.Join(authDir, "auth.json")
	if err := os.MkdirAll(authDir, 0o755); err != nil {
		return "", fmt.Errorf("create auth dir: %w", err)
	}

	if s.Broker != nil {
		lease, err := s.Broker.Acquire(context.Background(), credentials.AcquireRequest{
			RunID:  runID,
			UserID: requesterUserID,
		})
		if err == nil {
			if err := os.WriteFile(authPath, lease.AuthBlob, 0o600); err != nil {
				if releaseErr := s.Broker.Release(context.Background(), lease.ID); releaseErr != nil {
					log.Printf("release credential lease %s after auth write failure failed: %v", lease.ID, releaseErr)
				}
				return "", fmt.Errorf("write broker auth file: %w", err)
			}
			log.Printf("audit event=credential_lease_acquired run_id=%s credential_id=%s user_id=%s lease_id=%s strategy=%s", runID, lease.CredentialID, requesterUserID, lease.ID, lease.Strategy)
			return lease.ID, nil
		}
		if !errors.Is(err, credentials.ErrNoCredentialAvailable) {
			return "", fmt.Errorf("acquire broker credential: %w", err)
		}
		return "", credentials.ErrNoCredentialAvailable
	}
	return "", nil
}

func (s *Server) cleanupRunCredentialAuth(runDir, credentialLeaseID string) {
	if strings.TrimSpace(credentialLeaseID) != "" && s.Broker != nil {
		if err := s.Broker.Release(context.Background(), credentialLeaseID); err != nil {
			log.Printf("release credential lease %s failed: %v", credentialLeaseID, err)
		}
	}
	path := filepath.Join(runDir, "codex", "auth.json")
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		log.Printf("remove ephemeral auth file %s failed: %v", path, err)
	}
}

func (s *Server) activeCredentialLeaseIDForRun(runID string) string {
	lease, ok, err := s.Store.GetActiveCredentialLeaseByRunID(runID)
	if err != nil || !ok {
		return ""
	}
	return lease.ID
}

func (s *Server) ExecuteRun(runID string) {
	run, ok := s.Store.GetRun(runID)
	if !ok {
		return
	}
	if reason, ok := s.pendingRunCancelReason(runID); ok {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, reason)
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	if s.Store.IsTaskCompleted(run.TaskID) {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, "task is already completed")
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	if run.Status == state.StatusQueued {
		claimedRun, claimed, err := s.Store.ClaimRunStart(runID)
		if err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
			s.notifyRunTerminalGitHubBestEffort(updated)
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

	if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
		updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("claim run lease: %v", err))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}
	defer func() {
		if err := s.Store.DeleteRunLeaseForOwner(run.ID, s.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", run.ID, err)
		}
	}()

	runCredentialInfo, _ := s.Store.GetRunCredentialInfo(run.ID)
	requesterID := strings.TrimSpace(runCredentialInfo.CreatedByUserID)
	if requesterID == "" {
		requesterID = "system"
	}
	credentialLeaseID, err := s.prepareRunCredentialAuth(run.ID, run.RunDir, requesterID)
	if err != nil {
		updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("acquire credential lease: %v", err))
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}
	defer s.cleanupRunCredentialAuth(run.RunDir, credentialLeaseID)

	if reason, ok := s.pendingRunCancelReason(runID); ok {
		updated := s.setRunStatusWithFallback(run, state.StatusCanceled, reason)
		s.notifyRunTerminalGitHubBestEffort(updated)
		s.finishRun(updated)
		return
	}

	sessionMode := s.Config.EffectiveAgentSessionMode()
	if sessionMode != agent.SessionModeOff {
		s.cleanupAgentSessionsBestEffort()
	}

	sessionResume := agent.SessionEnabled(sessionMode, runtrigger.Normalize(run.Trigger.String()))
	sessionTaskKey := ""
	sessionTaskDir := ""
	backendSessionID := ""
	sessionRoot := strings.TrimSpace(s.Config.EffectiveAgentSessionRoot())
	if sessionRoot == "" {
		sessionRoot = filepath.Join(s.Config.DataDir, defaults.AgentSessionDirName)
	}
	if sessionResume {
		sessionTaskKey = agent.SessionTaskKey(run.Repo, run.TaskID)
		sessionTaskDir = filepath.Join(sessionRoot, sessionTaskKey)
		if existing, ok := s.Store.GetTaskAgentSession(run.TaskID); ok {
			if existing.AgentRuntime == run.AgentRuntime {
				backendSessionID = strings.TrimSpace(existing.RuntimeSessionID)
			} else if err := s.Store.DeleteTaskAgentSession(run.TaskID); err != nil {
				log.Printf("run %s failed to clear stale %s session for task %s: %v", run.ID, existing.AgentRuntime, run.TaskID, err)
			}
		}
		if backendSessionID == "" && run.AgentRuntime == agent.BackendGoose {
			backendSessionID = runner.SessionName(run.Repo, run.TaskID)
		}
		if err := os.MkdirAll(sessionTaskDir, 0o755); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("create agent session dir: %v", err))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
		if _, err := s.Store.UpsertTaskAgentSession(state.UpsertTaskAgentSessionInput{
			TaskID:           run.TaskID,
			AgentRuntime:     run.AgentRuntime,
			RuntimeSessionID: backendSessionID,
			SessionKey:       sessionTaskKey,
			SessionRoot:      sessionTaskDir,
			LastRunID:        run.ID,
		}); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist agent session: %v", err))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
	}

	spec := runner.Spec{
		RunID:        run.ID,
		TaskID:       run.TaskID,
		Repo:         run.Repo,
		Instruction:  run.Instruction,
		AgentRuntime: run.AgentRuntime,
		RunnerImage:  s.Config.RunnerImageForBackend(run.AgentRuntime),
		BaseBranch:   run.BaseBranch,
		HeadBranch:   run.HeadBranch,
		Trigger:      runtrigger.Normalize(run.Trigger.String()),
		RunDir:       run.RunDir,
		IssueNumber:  run.IssueNumber,
		PRNumber:     run.PRNumber,
		Context:      run.Context,
		Debug:        run.Debug,
		TaskSession: runner.SessionSpec{
			Mode:             sessionMode,
			Resume:           sessionResume,
			TaskDir:          sessionTaskDir,
			TaskKey:          sessionTaskKey,
			RuntimeSessionID: backendSessionID,
		},
	}
	log.Printf("run %s backend=%s session_mode=%s resume=%t key=%s session_id=%s", run.ID, run.AgentRuntime, sessionMode, sessionResume, sessionTaskKey, backendSessionID)
	execRec, hasExec := s.Store.GetRunExecution(run.ID)
	if !hasExec {
		// Persist a deterministic handle before launch so the next slot can
		// adopt the container even if this process exits mid-startup.
		pendingHandle := runner.ExecutionHandleForRun(run.ID)
		if _, err := s.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(pendingHandle.Backend))),
			ContainerName: pendingHandle.Name,
			ContainerID:   pendingHandle.Name,
			Status:        state.RunExecutionStatusCreated,
			ExitCode:      0,
		}); err != nil {
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist run execution: %v", err))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}

		handle, err := s.startDetachedWithRetry(context.Background(), spec)
		if err != nil {
			s.deleteRunExecutionBestEffort(run.ID)
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, err.Error())
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
		execRec, err = s.Store.UpsertRunExecution(state.RunExecution{
			RunID:         run.ID,
			Backend:       state.NormalizeRunExecutionBackend(state.RunExecutionBackend(string(handle.Backend))),
			ContainerName: strings.TrimSpace(handle.Name),
			ContainerID:   strings.TrimSpace(handle.ID),
			Status:        state.RunExecutionStatusRunning,
			ExitCode:      0,
		})
		if err != nil {
			s.stopRunExecutionBestEffort(run.ID, "failed to persist run execution")
			stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
			s.removeRunExecutionBestEffort(stopCtx, handle, run.ID, "cleanup failed persisted execution")
			stopCancel()
			updated := s.setRunStatusWithFallback(run, state.StatusFailed, fmt.Sprintf("persist run execution: %v", err))
			s.notifyRunTerminalGitHubBestEffort(updated)
			s.finishRun(updated)
			return
		}
	}

	if s.BeforeSupervise != nil {
		s.BeforeSupervise(run.ID)
	}
	s.PostRunStartCommentBestEffort(run, sessionMode, sessionResume)
	s.superviseDetachedRunLoop(run.ID, execRec, credentialLeaseID)
}

func (s *Server) superviseDetachedRunLoop(runID string, execRec state.RunExecution, credentialLeaseID string) {
	ctx, cancel := context.WithCancel(context.Background())
	s.mu.Lock()
	if s.StopSupervisors {
		s.mu.Unlock()
		cancel()
		return
	}
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
		if err := s.Store.DeleteRunLeaseForOwner(runID, s.InstanceID); err != nil {
			log.Printf("failed to delete run lease for %s: %v", runID, err)
		}
		if s.AfterRunCleanup != nil {
			s.AfterRunCleanup(runID)
		}
		if credentialLeaseID != "" {
			if err := s.Broker.Release(context.Background(), credentialLeaseID); err != nil {
				log.Printf("failed to release credential lease for %s: %v", runID, err)
			}
		}
	}()

	s.superviseRun(ctx, runID, execRec, credentialLeaseID)
}

func (s *Server) superviseRun(ctx context.Context, runID string, execRec state.RunExecution, credentialLeaseID string) {
	interval := s.supervisorTick()
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
	credentialRenewEvery := s.Config.CredentialRenewEvery
	if credentialRenewEvery <= 0 {
		credentialRenewEvery = 30 * time.Second
	}
	nextCredentialRenewAt := time.Now().UTC().Add(credentialRenewEvery)
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
				ok, err := s.Store.RenewRunLease(runID, s.InstanceID, runLeaseTTL)
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
			if credentialLeaseID != "" && !time.Now().UTC().Before(nextCredentialRenewAt) {
				if err := s.Broker.Renew(ctx, credentialLeaseID); err != nil {
					log.Printf("run %s credential lease renew failed: %v", runID, err)
					if cancelErr := s.Store.RequestRunCancel(runID, "credential lease lost", "broker"); cancelErr != nil {
						log.Printf("run %s request cancel after credential lease loss failed: %v", runID, cancelErr)
					}
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := s.Launcher.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop after credential lease loss failed: %v", runID, stopErr)
						}
						stopRequested = true
					}
				}
				nextCredentialRenewAt = time.Now().UTC().Add(credentialRenewEvery)
			}

			now := time.Now().UTC()
			execState, err := s.Launcher.Inspect(ctx, handle)
			if errors.Is(err, runner.ErrExecutionNotFound) {
				run, ok := s.Store.GetRun(runID)
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
				execStatus := state.RunExecutionStatusRunning
				if reason, ok := s.pendingRunCancelReason(runID); ok {
					execStatus = state.RunExecutionStatusStopping
					if !stopRequested {
						stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
						stopErr := s.Launcher.Stop(stopCtx, handle, 10*time.Second)
						stopCancel()
						if stopErr != nil && !errors.Is(stopErr, runner.ErrExecutionNotFound) && !errors.Is(stopErr, context.Canceled) {
							log.Printf("run %s stop failed: %v", runID, stopErr)
						}
						log.Printf("run %s cancel requested: %s", runID, reason)
						stopRequested = true
					}
				}
				if _, err := s.Store.UpdateRunExecutionState(runID, execStatus, 0, now); err != nil {
					log.Printf("run %s update execution state %q failed: %v", runID, execStatus, err)
				}
				continue
			}

			exitCode := 0
			if execState.ExitCode != nil {
				exitCode = *execState.ExitCode
			}
			if _, err := s.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusExited, exitCode, now); err != nil {
				log.Printf("run %s update execution exited state failed: %v", runID, err)
			}
			s.finalizeDetachedRun(runID, execRec, exitCode)
			return
		}
	}
}

func (s *Server) finalizeDetachedRun(runID string, execRec state.RunExecution, observedExitCode int) {
	run, ok := s.Store.GetRun(runID)
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
	if strings.TrimSpace(meta.TaskSessionID) != "" {
		existing, _ := s.Store.GetTaskAgentSession(run.TaskID)
		sessionKey := ""
		sessionRoot := ""
		if existing.AgentRuntime == run.AgentRuntime {
			sessionKey = existing.SessionKey
			sessionRoot = existing.SessionRoot
		}
		if _, err := s.Store.UpsertTaskAgentSession(state.UpsertTaskAgentSessionInput{
			TaskID:           run.TaskID,
			AgentRuntime:     run.AgentRuntime,
			RuntimeSessionID: strings.TrimSpace(meta.TaskSessionID),
			SessionKey:       sessionKey,
			SessionRoot:      sessionRoot,
			LastRunID:        run.ID,
		}); err != nil {
			log.Printf("run %s failed to persist resolved task session id %q: %v", run.ID, meta.TaskSessionID, err)
		}
	}

	runFailed := meta.ExitCode != 0 || strings.TrimSpace(meta.Error) != ""
	if runFailed {
		if retryAt, reason, ok := detectUsageLimitPause(run, meta.Error); ok {
			effectiveRetryAt := s.pauseWorkersUntil(retryAt, fmt.Sprintf("run %s hit provider usage limit: %s", run.ID, reason))
			if err := s.requeueRun(run.ID); err != nil {
				log.Printf("run %s usage-limit requeue failed: %v", run.ID, err)
			} else {
				log.Printf("run %s requeued after usage limit; scheduling resumes at %s", run.ID, effectiveRetryAt.Format(time.RFC3339))
				s.cleanupDetachedExecution(runID, execRec)
				if updated, ok := s.Store.GetRun(run.ID); ok {
					s.finishRun(updated)
					return
				}
				run.Status = state.StatusQueued
				run.Error = ""
				run.StartedAt = nil
				run.CompletedAt = nil
				s.finishRun(run)
				return
			}
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
	tokenUsage, hasTokenUsage, tokenUsageErr := loadRunTokenUsage(run)
	if tokenUsageErr != nil {
		log.Printf("run %s parse token usage failed: %v", run.ID, tokenUsageErr)
	}

	now := time.Now().UTC()
	updated, err := s.Store.UpdateRun(run.ID, func(r *state.Run) error {
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
	if hasTokenUsage {
		if _, err := s.Store.UpsertRunTokenUsage(tokenUsage); err != nil {
			log.Printf("run %s persist token usage failed: %v", updated.ID, err)
		}
	}

	s.notifyRunTerminalGitHubBestEffort(updated)
	if updated.PRNumber > 0 {
		s.setTaskPRBestEffort(updated.TaskID, updated.Repo, updated.PRNumber)
	}
	if updated.Status == state.StatusFailed {
		info, ok := s.Store.GetRunCredentialInfo(updated.ID)
		if ok && strings.TrimSpace(info.CredentialID) != "" && isCredentialAuthFailure(updated.Error) {
			until := time.Now().UTC().Add(5 * time.Minute)
			if err := s.Store.SetCodexCredentialStatus(info.CredentialID, state.CredentialStatusCooldown, &until, updated.Error); err != nil {
				log.Printf("run %s set credential cooldown failed: %v", updated.ID, err)
			} else {
				log.Printf("audit event=credential_cooldown run_id=%s credential_id=%s until=%s", updated.ID, info.CredentialID, until.Format(time.RFC3339))
			}
		}
	}
	s.cleanupDetachedExecution(runID, execRec)
	s.finishRun(updated)
}

func (s *Server) cleanupDetachedExecution(runID string, execRec state.RunExecution) {
	removeCtx, removeCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.Launcher.Remove(removeCtx, runExecutionHandle(execRec))
	removeCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s remove detached container failed: %v", runID, err)
	}
	if err := s.Store.DeleteRunExecution(runID); err != nil {
		log.Printf("run %s clear execution state failed: %v", runID, err)
	}
	if err := s.Store.DeleteRunLease(runID); err != nil {
		log.Printf("run %s clear run lease failed: %v", runID, err)
	}
	if run, ok := s.Store.GetRun(runID); ok {
		authPath := filepath.Join(run.RunDir, "codex", "auth.json")
		if err := os.Remove(authPath); err != nil && !errors.Is(err, os.ErrNotExist) {
			log.Printf("run %s remove auth file failed: %v", runID, err)
		}
	}
}

func (s *Server) stopRunExecutionBestEffort(runID string, note string) {
	execRec, ok := s.Store.GetRunExecution(runID)
	if !ok {
		return
	}
	if _, err := s.Store.UpdateRunExecutionState(runID, state.RunExecutionStatusStopping, execRec.ExitCode, time.Now().UTC()); err != nil {
		log.Printf("run %s mark execution stopping failed: %v", runID, err)
	}
	stopCtx, stopCancel := context.WithTimeout(context.Background(), 15*time.Second)
	err := s.Launcher.Stop(stopCtx, runExecutionHandle(execRec), 10*time.Second)
	stopCancel()
	if err != nil && !errors.Is(err, runner.ErrExecutionNotFound) && !errors.Is(err, context.Canceled) {
		log.Printf("run %s stop execution failed (%s): %v", runID, note, err)
	}
}

func runExecutionHandle(execRec state.RunExecution) runner.ExecutionHandle {
	return runner.ExecutionHandle{
		Backend: runner.ExecutionBackend(strings.TrimSpace(string(execRec.Backend))),
		ID:      strings.TrimSpace(execRec.ContainerID),
		Name:    strings.TrimSpace(execRec.ContainerName),
	}
}

func (s *Server) startDetachedWithRetry(ctx context.Context, spec runner.Spec) (runner.ExecutionHandle, error) {
	maxAttempts := s.Config.RunnerMaxAttempts
	if maxAttempts <= 0 {
		maxAttempts = 1
	}
	var (
		handle runner.ExecutionHandle
		err    error
	)
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		if err := ctx.Err(); err != nil {
			return handle, fmt.Errorf("check start-detached context: %w", err)
		}
		handle, err = s.Launcher.StartDetached(ctx, spec)
		if err == nil {
			return handle, nil
		}
		if errors.Is(ctx.Err(), context.Canceled) || errors.Is(err, context.Canceled) {
			return handle, context.Canceled
		}
		if attempt == maxAttempts {
			break
		}
		backoff := s.startRetryBackoff(attempt)
		log.Printf("run %s attempt %d/%d failed: %v (retrying in %s)", spec.RunID, attempt, maxAttempts, err, backoff)
		timer := time.NewTimer(backoff)
		select {
		case <-ctx.Done():
			timer.Stop()
			return handle, context.Canceled
		case <-timer.C:
		}
	}
	return handle, fmt.Errorf("start detached run %s: %w", spec.RunID, err)
}

func (s *Server) StopRunSupervisors() {
	s.mu.Lock()
	s.StopSupervisors = true
	cancels := make([]context.CancelFunc, 0, len(s.runCancels))
	for _, cancel := range s.runCancels {
		cancels = append(cancels, cancel)
	}
	s.mu.Unlock()
	for _, cancel := range cancels {
		cancel()
	}
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

func (s *Server) ScheduleRuns(preferredTaskID string) {
	if s.isDraining() {
		return
	}
	preferredTaskID = strings.TrimSpace(preferredTaskID)

	if pauseUntil, pauseReason, paused := s.activeWorkerPause(); paused {
		s.ensureResumeTimer(pauseUntil)
		log.Printf("run scheduling paused until %s: %s", pauseUntil.Format(time.RFC3339), pauseReason)
		return
	}

	s.scheduleMu.Lock()
	defer s.scheduleMu.Unlock()

	for {
		if pauseUntil, pauseReason, paused := s.activeWorkerPause(); paused {
			s.ensureResumeTimer(pauseUntil)
			log.Printf("run scheduling paused until %s: %s", pauseUntil.Format(time.RFC3339), pauseReason)
			return
		}
		atCapacity := s.ActiveRunCount() >= s.concurrencyLimit()
		draining := s.isDraining()
		if draining || atCapacity {
			return
		}

		run, claimed, err := s.Store.ClaimNextQueuedRun(preferredTaskID)
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
		if err := s.Store.UpsertRunLease(run.ID, s.InstanceID, runLeaseTTL); err != nil {
			s.setRunStatusBestEffort(run.ID, state.StatusFailed, fmt.Sprintf("claim run lease: %v", err))
			continue
		}

		go s.ExecuteRun(run.ID)
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
		s.updateRunBestEffort(run.ID, func(r *state.Run) error {
			if r.PRStatus == state.PRStatusMerged {
				return nil
			}
			r.PRStatus = state.PRStatusOpen
			return nil
		})
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
	if !ok || task.Status != state.TaskOpen {
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
		target, ok, err := LoadRunResponseTarget(run.RunDir)
		if err != nil {
			log.Printf("run %s load response target for review thread cancellation failed: %v", run.ID, err)
			continue
		}
		if !ok || target.ReviewThreadID != reviewThreadID {
			continue
		}
		s.setRunStatusBestEffort(run.ID, state.StatusCanceled, reason)
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

func writeJSON[T any](w http.ResponseWriter, status int, v T) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(v); err != nil {
		log.Printf("write JSON response failed: %v", err)
	}
}

func LogRequests(next http.Handler) http.Handler {
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

func WithRequestID(next http.Handler) http.Handler {
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

func (s *Server) cancelRunningTaskRuns(taskID, reason string) {
	taskID = strings.TrimSpace(taskID)
	if taskID == "" {
		return
	}
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRunningRuns() {
		if run.TaskID != taskID {
			continue
		}
		if err := s.Store.RequestRunCancel(run.ID, reason, "issue"); err != nil {
			log.Printf("failed to request run cancel for %s: %v", run.ID, err)
			continue
		}
		s.stopRunExecutionBestEffort(run.ID, "task cancellation")
	}
}

func (s *Server) CancelActiveRuns(reason string) {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		reason = "canceled"
	}
	for _, run := range s.Store.ListRunningRuns() {
		s.requestRunCancelBestEffort(run.ID, reason, "shutdown")
		s.stopRunExecutionBestEffort(run.ID, "shutdown cancellation")
	}
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

func (s *Server) cleanupAgentSessionsBestEffort() {
	ttlDays := s.Config.EffectiveAgentSessionTTLDays()
	if ttlDays <= 0 {
		return
	}
	root := strings.TrimSpace(s.Config.EffectiveAgentSessionRoot())
	if root == "" {
		root = filepath.Join(s.Config.DataDir, defaults.AgentSessionDirName)
	}
	removed, err := CleanupStaleAgentSessionDirs(root, ttlDays, time.Now().UTC())
	if err != nil {
		log.Printf("agent session cleanup warning: root=%s ttl_days=%d error=%v", root, ttlDays, err)
		return
	}
	if removed > 0 {
		log.Printf("agent session cleanup: root=%s ttl_days=%d removed=%d", root, ttlDays, removed)
	}
}

func CleanupStaleAgentSessionDirs(root string, ttlDays int, now time.Time) (int, error) {
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
		return 0, fmt.Errorf("read agent session directory %s: %w", root, err)
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
				firstErr = fmt.Errorf("stat agent session entry %s: %w", entry.Name(), infoErr)
			}
			continue
		}
		if info.ModTime().After(cutoff) {
			continue
		}
		path := filepath.Join(root, entry.Name())
		if rmErr := os.RemoveAll(path); rmErr != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("remove stale agent session dir %s: %w", path, rmErr)
			}
			continue
		}
		removed++
	}
	return removed, firstErr
}

func resolveRunAgentLogPath(runDir string) (string, error) {
	primary := filepath.Join(strings.TrimSpace(runDir), agentLogFile)
	if info, err := os.Stat(primary); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("agent log path is a directory: %s", primary)
		}
		return primary, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat agent log path %s: %w", primary, err)
	}

	legacy := filepath.Join(strings.TrimSpace(runDir), legacyAgentLogFile)
	if info, err := os.Stat(legacy); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("legacy agent log path is a directory: %s", legacy)
		}
		return legacy, nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("stat legacy agent log path %s: %w", legacy, err)
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

func LoadRunResponseTarget(runDir string) (RunResponseTarget, bool, error) {
	path := filepath.Join(strings.TrimSpace(runDir), RunResponseTargetFile)
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return RunResponseTarget{}, false, nil
		}
		return RunResponseTarget{}, false, fmt.Errorf("read run response target: %w", err)
	}
	var target RunResponseTarget
	if err := json.Unmarshal(data, &target); err != nil {
		return RunResponseTarget{}, false, fmt.Errorf("decode run response target: %w", err)
	}
	target.Repo = strings.TrimSpace(target.Repo)
	target.RequestedBy = strings.TrimSpace(target.RequestedBy)
	target.Trigger = runtrigger.Normalize(target.Trigger.String())
	return target, true, nil
}

func RunCommentMarkerPath(runDir, markerFile string) string {
	return filepath.Join(strings.TrimSpace(runDir), markerFile)
}

func RunStartCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runStartCommentMarkerFile)
}

func RunCompletionCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runCompletionCommentMarkerFile)
}

func RunFailureCommentMarkerPath(runDir string) string {
	return RunCommentMarkerPath(runDir, runFailureCommentMarkerFile)
}

func RunCommentMarkerExists(runDir, markerFile, markerKind string) (bool, error) {
	path := RunCommentMarkerPath(runDir, markerFile)
	info, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, nil
		}
		return false, fmt.Errorf("stat %s marker: %w", markerKind, err)
	}
	if info.IsDir() {
		return false, fmt.Errorf("%s marker path is a directory: %s", markerKind, path)
	}
	return true, nil
}

func runStartCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runStartCommentMarkerFile, "start comment")
}

func runCompletionCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runCompletionCommentMarkerFile, "completion comment")
}

func runFailureCommentMarkerExists(runDir string) (bool, error) {
	return RunCommentMarkerExists(runDir, runFailureCommentMarkerFile, "failure comment")
}

func writeRunCommentMarker(run state.Run, repo string, issueNumber int, markerFile, markerKind string) error {
	marker := RunCommentMarker{
		RunID:       run.ID,
		Repo:        strings.TrimSpace(repo),
		IssueNumber: issueNumber,
		PostedAt:    time.Now().UTC().Format(time.RFC3339Nano),
	}
	data, err := json.MarshalIndent(marker, "", "  ")
	if err != nil {
		return fmt.Errorf("encode %s marker: %w", markerKind, err)
	}
	path := RunCommentMarkerPath(run.RunDir, markerFile)
	if err := writeFileAtomically(path, data, 0o644); err != nil {
		return fmt.Errorf("write %s marker: %w", markerKind, err)
	}
	return nil
}

func writeRunStartCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runStartCommentMarkerFile, "start comment")
}

func writeRunCompletionCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runCompletionCommentMarkerFile, "completion comment")
}

func writeRunFailureCommentMarker(run state.Run, repo string, issueNumber int) error {
	return writeRunCommentMarker(run, repo, issueNumber, runFailureCommentMarkerFile, "failure comment")
}

func writeFileAtomically(path string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(path)
	tempFile, err := os.CreateTemp(dir, "."+filepath.Base(path)+".tmp-*")
	if err != nil {
		return fmt.Errorf("create temp file for %s: %w", path, err)
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
		return fmt.Errorf("write temp file for %s: %w", path, err)
	}
	if err := tempFile.Chmod(mode); err != nil {
		if closeErr := tempFile.Close(); closeErr != nil {
			return fmt.Errorf("chmod temp file: %w (close temp file: %v)", err, closeErr)
		}
		return fmt.Errorf("chmod temp file for %s: %w", path, err)
	}
	if err := tempFile.Close(); err != nil {
		return fmt.Errorf("close temp file for %s: %w", path, err)
	}
	if err := os.Rename(tempPath, path); err != nil {
		return fmt.Errorf("rename temp file to %s: %w", path, err)
	}
	removeTemp = false
	return nil
}

func requesterForRun(run state.Run, target RunResponseTarget, requesterUserID string) string {
	requestedBy := strings.TrimSpace(target.RequestedBy)
	if requestedBy != "" {
		return requestedBy
	}
	requesterUserID = strings.TrimSpace(requesterUserID)
	if requesterUserID == "" || requesterUserID == "system" {
		return ""
	}
	return requesterUserID
}

func (s *Server) PostRunStartCommentBestEffort(run state.Run, sessionMode agent.SessionMode, sessionResume bool) {
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if markerExists, err := runStartCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check start comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	runCredentialInfo, _ := s.Store.GetRunCredentialInfo(run.ID)
	body := buildRunStartComment(run, target, requesterForRun(run, target, runCredentialInfo.CreatedByUserID), sessionMode, sessionResume)

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post start comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunStartCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist start comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) PostRunCompletionCommentBestEffort(run state.Run) {
	if !isCommentTriggeredRun(run.Trigger) {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
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

	var totalTokens *int64
	if usage, ok := s.Store.GetRunTokenUsage(run.ID); ok && usage.TotalTokens > 0 {
		totalTokens = &usage.TotalTokens
	}

	body, err := buildRunCompletionComment(run, target, repo, totalTokens)
	if err != nil {
		log.Printf("failed to build completion comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post completion comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunCompletionCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist completion comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) PostRunFailureCommentBestEffort(run state.Run) {
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}

	target, ok, err := LoadRunResponseTarget(run.RunDir)
	if err != nil {
		log.Printf("failed to load run response target for %s: %v", run.ID, err)
	}
	if !ok {
		target = RunResponseTarget{}
	}
	if markerExists, err := runFailureCommentMarkerExists(run.RunDir); err != nil {
		log.Printf("failed to check failure comment marker for run %s: %v", run.ID, err)
		return
	} else if markerExists {
		return
	}

	repo, issueNumber := resolveRunCommentTarget(run, target)
	if repo == "" || issueNumber <= 0 {
		return
	}

	body, err := buildRunFailureComment(run, target)
	if err != nil {
		log.Printf("failed to build failure comment for %s: %v", run.ID, err)
		return
	}

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if err := s.GitHub.CreateIssueComment(ctx, repo, issueNumber, body); err != nil {
		log.Printf("failed to post failure comment for run %s on %s#%d: %v", run.ID, repo, issueNumber, err)
		return
	}
	if err := writeRunFailureCommentMarker(run, repo, issueNumber); err != nil {
		log.Printf("failed to persist failure comment marker for run %s: %v", run.ID, err)
	}
}

func (s *Server) notifyRunTerminalGitHubBestEffort(run state.Run) {
	switch run.Status {
	case state.StatusSucceeded:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionRocket)
		s.PostRunCompletionCommentBestEffort(run)
	case state.StatusReview:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionHooray)
		s.PostRunCompletionCommentBestEffort(run)
	case state.StatusFailed:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionConfused)
		s.PostRunFailureCommentBestEffort(run)
	case state.StatusCanceled:
		s.addIssueReactionBestEffort(run.Repo, run.IssueNumber, ghapi.ReactionMinusOne)
	}
}

func resolveRunCommentTarget(run state.Run, target RunResponseTarget) (string, int) {
	repo := strings.TrimSpace(target.Repo)
	if repo == "" {
		repo = strings.TrimSpace(run.Repo)
	}
	issueNumber := target.IssueNumber
	if issueNumber <= 0 {
		if isCommentTriggeredRun(run.Trigger) && run.PRNumber > 0 {
			issueNumber = run.PRNumber
		} else {
			issueNumber = run.IssueNumber
		}
	}
	return repo, issueNumber
}

func buildRunStartComment(run state.Run, target RunResponseTarget, requestedBy string, sessionMode agent.SessionMode, sessionResume bool) string {
	var queueDelaySeconds *int64
	if run.StartedAt != nil {
		delay := int64(run.StartedAt.UTC().Sub(run.CreatedAt.UTC()).Seconds())
		if delay < 0 {
			delay = 0
		}
		queueDelaySeconds = &delay
	}

	return ghapi.RenderStartComment(runStartCommentBodyMarker, runsummary.StartCommentInput{
		RunID:             run.ID,
		RequestedBy:       requestedBy,
		Trigger:           runtrigger.Normalize(firstNonEmpty(target.Trigger.String(), run.Trigger.String())),
		AgentRuntime:      run.AgentRuntime,
		RunnerCommit:      loadRunBuildCommit(run.RunDir),
		BaseBranch:        run.BaseBranch,
		HeadBranch:        run.HeadBranch,
		SessionMode:       string(sessionMode),
		SessionResume:     sessionResume,
		Debug:             run.Debug,
		Instruction:       run.Instruction,
		Context:           run.Context,
		QueueDelaySeconds: queueDelaySeconds,
	})
}

func loadRunBuildCommit(runDir string) string {
	if strings.TrimSpace(runDir) == "" {
		return ""
	}
	metaPath := filepath.Join(runDir, "meta.json")
	deadline := time.Now().UTC().Add(250 * time.Millisecond)
	for {
		meta, err := runner.ReadMeta(metaPath)
		if err == nil {
			return strings.TrimSpace(meta.BuildCommit)
		}
		if !time.Now().UTC().Before(deadline) {
			return ""
		}
		time.Sleep(25 * time.Millisecond)
	}
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func buildRunCompletionComment(run state.Run, target RunResponseTarget, repo string, totalTokens *int64) (string, error) {
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
	body, err := ghapi.RenderCompletionComment(runCompletionCommentBodyMarker, runsummary.CompletionCommentInput{
		RunID:           run.ID,
		Repo:            repo,
		RequestedBy:     target.RequestedBy,
		HeadSHA:         run.HeadSHA,
		IssueNumber:     run.IssueNumber,
		GooseOutput:     agentOutput,
		CommitMessage:   commitMessageData,
		DurationSeconds: runsummary.RunDurationSeconds(run.CreatedAt, run.StartedAt, run.CompletedAt),
		TotalTokens:     totalTokens,
	})
	if err != nil {
		return "", fmt.Errorf("render completion comment: %w", err)
	}
	return body, nil
}

func loadRunTokenUsage(run state.Run) (state.RunTokenUsage, bool, error) {
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return state.RunTokenUsage{}, false, nil
		}
		return state.RunTokenUsage{}, false, fmt.Errorf("resolve agent log: %w", err)
	}
	data, err := os.ReadFile(agentPath)
	if err != nil {
		return state.RunTokenUsage{}, false, fmt.Errorf("read agent log: %w", err)
	}
	usage, ok := runsummary.ExtractTokenUsage(string(data))
	if !ok {
		return state.RunTokenUsage{}, false, nil
	}

	return state.RunTokenUsage{
		RunID:                 run.ID,
		AgentRuntime:          run.AgentRuntime,
		Provider:              usage.Provider,
		Model:                 usage.Model,
		TotalTokens:           usage.TotalTokens,
		InputTokens:           usage.InputTokens,
		OutputTokens:          usage.OutputTokens,
		CachedInputTokens:     usage.CachedInputTokens,
		ReasoningOutputTokens: usage.ReasoningOutputTokens,
		RawUsageJSON:          usage.RawUsageJSON,
		CapturedAt:            time.Now().UTC(),
	}, true, nil
}

func buildRunFailureComment(run state.Run, target RunResponseTarget) (string, error) {
	agentOutput := ""
	agentLogLabel := "Agent log"
	agentPath, err := resolveRunAgentLogPath(run.RunDir)
	if err == nil {
		if data, readErr := os.ReadFile(agentPath); readErr == nil {
			agentOutput = string(data)
			if filepath.Base(agentPath) == legacyAgentLogFile {
				agentLogLabel = "Goose log"
			}
		} else if !os.IsNotExist(readErr) {
			return "", fmt.Errorf("read agent log: %w", readErr)
		}
	} else if !os.IsNotExist(err) {
		return "", fmt.Errorf("resolve agent log: %w", err)
	}

	summary := summarizeRunFailure(run, agentOutput)
	header := summary.Headline
	if requestedBy := strings.TrimSpace(target.RequestedBy); requestedBy != "" {
		header = fmt.Sprintf("@%s %s", requestedBy, header)
	}

	return ghapi.RenderFailureComment(
		runFailureCommentBodyMarker,
		header,
		summary.RetryAt,
		summary.Reason,
		buildRunFailureDetails(run.Error, agentOutput, agentLogLabel),
	), nil
}

func summarizeRunFailure(run state.Run, agentOutput string) RunFailureSummary {
	corpusParts := make([]string, 0, 2)
	if reason := strings.TrimSpace(run.Error); reason != "" {
		corpusParts = append(corpusParts, reason)
	}
	if output := strings.TrimSpace(agentOutput); output != "" {
		corpusParts = append(corpusParts, output)
	}
	corpus := strings.Join(corpusParts, "\n")
	if usageLimitPattern.MatchString(corpus) {
		summary := RunFailureSummary{
			Headline: fmt.Sprintf("Rascal run `%s` failed because Goose hit the Codex usage limit.", run.ID),
		}
		if matches := retryAtPattern.FindStringSubmatch(corpus); len(matches) == 2 {
			summary.RetryAt = strings.TrimSpace(matches[1])
		}
		return summary
	}

	reason := compactFailureReason(run.Error)
	if reason == "" {
		reason = "The runner exited without a more specific error message."
	}
	return RunFailureSummary{
		Headline: fmt.Sprintf("Rascal run `%s` failed.", run.ID),
		Reason:   reason,
	}
}

func compactFailureReason(reason string) string {
	reason = strings.TrimSpace(reason)
	if reason == "" {
		return ""
	}
	reason = strings.Join(strings.Fields(reason), " ")
	const maxReasonLen = 280
	if len(reason) <= maxReasonLen {
		return reason
	}
	return strings.TrimSpace(reason[:maxReasonLen-3]) + "..."
}

func buildRunFailureDetails(runError, agentOutput, agentLogLabel string) string {
	parts := make([]string, 0, 2)
	if reason := strings.TrimSpace(runError); reason != "" {
		parts = append(parts, "Run error:\n"+reason)
	}
	if output := strings.TrimSpace(agentOutput); output != "" {
		parts = append(parts, agentLogLabel+":\n"+output)
	}
	return strings.Join(parts, "\n\n")
}

func detectUsageLimitPause(run state.Run, errText string) (time.Time, string, bool) {
	corpusParts := make([]string, 0, 2)
	if reason := strings.TrimSpace(errText); reason != "" {
		corpusParts = append(corpusParts, reason)
	}
	if output, loadErr := loadRunAgentOutput(run.RunDir); loadErr == nil && strings.TrimSpace(output) != "" {
		corpusParts = append(corpusParts, output)
	} else if loadErr != nil && !errors.Is(loadErr, os.ErrNotExist) {
		log.Printf("run %s read agent output for usage-limit detection failed: %v", run.ID, loadErr)
	}

	corpus := strings.Join(corpusParts, "\n")
	if !usageLimitPattern.MatchString(corpus) {
		return time.Time{}, "", false
	}

	retryAt, reason := ParseUsageLimitRetryAt(corpus, time.Now().UTC())
	if retryAt.IsZero() {
		retryAt = time.Now().UTC().Add(defaultUsageLimitPause)
		if reason == "" {
			reason = fmt.Sprintf("usage limit without retry timestamp; applying default pause of %s", defaultUsageLimitPause)
		}
	}
	return retryAt, reason, true
}

func loadRunAgentOutput(runDir string) (string, error) {
	runDir = strings.TrimSpace(runDir)
	outputPath := filepath.Join(runDir, agentOutputFile)
	if data, err := os.ReadFile(outputPath); err == nil {
		return string(data), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read agent output %s: %w", outputPath, err)
	}

	legacyPath := filepath.Join(runDir, legacyAgentLogFile)
	if data, err := os.ReadFile(legacyPath); err == nil {
		return string(data), nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", fmt.Errorf("read legacy agent log %s: %w", legacyPath, err)
	}

	return "", os.ErrNotExist
}

func ParseUsageLimitRetryAt(corpus string, now time.Time) (time.Time, string) {
	matches := retryAtPattern.FindStringSubmatch(corpus)
	if len(matches) == 2 {
		raw := sanitizeRetryHint(matches[1])
		if raw != "" {
			if retryAt, ok := parseAbsoluteRetryTime(raw, now); ok {
				return retryAt, fmt.Sprintf("provider requested retry at %s", raw)
			}
		}
	}

	matches = retryInPattern.FindStringSubmatch(corpus)
	if len(matches) == 2 {
		raw := sanitizeRetryHint(matches[1])
		if raw != "" {
			if delay, ok := parseRetryDelay(raw); ok {
				if now.IsZero() {
					now = time.Now().UTC()
				}
				return now.Add(delay), fmt.Sprintf("provider requested retry in %s", raw)
			}
		}
	}

	return time.Time{}, ""
}

func sanitizeRetryHint(raw string) string {
	raw = strings.TrimSpace(raw)
	raw = strings.Trim(raw, " .,:;)")
	raw = strings.TrimPrefix(raw, "(")
	raw = strings.Join(strings.Fields(raw), " ")
	return ordinalDayPattern.ReplaceAllString(raw, "$1")
}

func parseAbsoluteRetryTime(raw string, now time.Time) (time.Time, bool) {
	if raw == "" {
		return time.Time{}, false
	}

	if retryAt, err := time.Parse(time.RFC3339, raw); err == nil {
		return normalizeFutureRetryTime(retryAt, raw, now)
	}
	if retryAt, err := time.Parse(time.RFC3339Nano, raw); err == nil {
		return normalizeFutureRetryTime(retryAt, raw, now)
	}

	layouts := []string{
		"Jan 2, 2006 3:04 PM",
		"January 2, 2006 3:04 PM",
		"Jan 2 2006 3:04 PM",
		"January 2 2006 3:04 PM",
		"Jan 2, 2006 15:04",
		"January 2, 2006 15:04",
		"Jan 2 2006 15:04",
		"January 2 2006 15:04",
	}
	for _, loc := range []*time.Location{time.Local, time.UTC} {
		for _, layout := range layouts {
			retryAt, err := time.ParseInLocation(layout, raw, loc)
			if err != nil {
				continue
			}
			return normalizeFutureRetryTime(retryAt, raw, now)
		}
	}

	zonedLayouts := []string{
		"Jan 2, 2006 3:04 PM MST",
		"January 2, 2006 3:04 PM MST",
		"Jan 2 2006 3:04 PM MST",
		"January 2 2006 3:04 PM MST",
		"Jan 2, 2006 15:04 MST",
		"January 2, 2006 15:04 MST",
		"Jan 2 2006 15:04 MST",
		"January 2 2006 15:04 MST",
		"Jan 2, 2006 3:04 PM -0700",
		"January 2, 2006 3:04 PM -0700",
		"Jan 2 2006 3:04 PM -0700",
		"January 2 2006 3:04 PM -0700",
		"Jan 2, 2006 15:04 -0700",
		"January 2, 2006 15:04 -0700",
		"Jan 2 2006 15:04 -0700",
		"January 2 2006 15:04 -0700",
	}
	for _, layout := range zonedLayouts {
		retryAt, err := time.Parse(layout, raw)
		if err != nil {
			continue
		}
		return normalizeFutureRetryTime(retryAt, raw, now)
	}

	return time.Time{}, false
}

func normalizeFutureRetryTime(retryAt time.Time, raw string, now time.Time) (time.Time, bool) {
	if retryAt.IsZero() {
		return time.Time{}, false
	}
	retryAt = retryAt.UTC()
	if !now.IsZero() && !retryAt.After(now) {
		return now.Add(minimumUsageLimitPause), true
	}
	return retryAt, true
}

func parseRetryDelay(raw string) (time.Duration, bool) {
	if raw == "" {
		return 0, false
	}
	if d, err := time.ParseDuration(strings.ReplaceAll(strings.ToLower(raw), " ", "")); err == nil && d > 0 {
		return d, true
	}

	matches := durationComponentPattern.FindAllStringSubmatch(raw, -1)
	if len(matches) == 0 {
		return 0, false
	}

	var total time.Duration
	for _, match := range matches {
		value, err := strconv.Atoi(match[1])
		if err != nil {
			return 0, false
		}
		switch unit := strings.ToLower(match[2]); {
		case strings.HasPrefix(unit, "d"):
			total += time.Duration(value) * 24 * time.Hour
		case strings.HasPrefix(unit, "h"):
			total += time.Duration(value) * time.Hour
		case strings.HasPrefix(unit, "m"):
			total += time.Duration(value) * time.Minute
		case strings.HasPrefix(unit, "s"):
			total += time.Duration(value) * time.Second
		default:
			return 0, false
		}
	}
	if total <= 0 {
		return 0, false
	}
	return total, true
}

func (s *Server) requeueRun(runID string) error {
	_, err := s.Store.UpdateRun(runID, func(r *state.Run) error {
		if r.Status != state.StatusRunning {
			return nil
		}
		r.Status = state.StatusQueued
		r.Error = ""
		r.StartedAt = nil
		r.CompletedAt = nil
		return nil
	})
	if err != nil {
		return fmt.Errorf("requeue run %q: %w", runID, err)
	}
	return nil
}

func (s *Server) activeWorkerPause() (time.Time, string, bool) {
	pauseUntil, reason, ok, err := s.Store.ActiveSchedulerPause(workerPauseScope, time.Now().UTC())
	if err != nil {
		log.Printf("load active worker pause failed: %v", err)
		return time.Time{}, "", false
	}
	return pauseUntil, reason, ok
}

func (s *Server) pauseWorkersUntil(until time.Time, reason string) time.Time {
	if until.IsZero() {
		until = time.Now().UTC().Add(defaultUsageLimitPause)
	}
	effective, err := s.Store.PauseScheduler(workerPauseScope, reason, until)
	if err != nil {
		log.Printf("persist worker pause until %s failed: %v", until.Format(time.RFC3339), err)
		effective = until.UTC()
	}
	s.ensureResumeTimer(effective)
	return effective
}

func (s *Server) ensureResumeTimer(until time.Time) {
	if until.IsZero() {
		return
	}
	until = until.UTC()
	delay := time.Until(until)
	if delay < 0 {
		delay = 0
	}

	s.mu.Lock()
	if s.draining {
		s.mu.Unlock()
		return
	}
	if !s.resumeAt.IsZero() && s.resumeAt.Equal(until) {
		s.mu.Unlock()
		return
	}
	if s.resumeTimer != nil {
		s.resumeTimer.Stop()
	}
	s.resumeAt = until
	s.resumeTimer = time.AfterFunc(delay, func() {
		s.mu.Lock()
		if !s.resumeAt.Equal(until) {
			s.mu.Unlock()
			return
		}
		s.resumeAt = time.Time{}
		s.resumeTimer = nil
		draining := s.draining
		s.mu.Unlock()
		if draining {
			return
		}
		s.ScheduleRuns("")
	})
	s.mu.Unlock()
}

func (s *Server) addIssueReactionBestEffort(repo string, issueNumber int, reaction string) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddIssueReaction(ctx, repo, issueNumber, reaction); err != nil {
		log.Printf("failed to add %q reaction for %s#%d: %v", reaction, repo, issueNumber, err)
	}
}

func (s *Server) removeIssueReactionsBestEffort(repo string, issueNumber int) {
	if issueNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.RemoveIssueReactions(ctx, repo, issueNumber); err != nil {
		log.Printf("failed to remove reactions for %s#%d: %v", repo, issueNumber, err)
	}
}

func (s *Server) addIssueCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddIssueCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for issue comment %d in %s: %v", reaction, commentID, repo, err)
	}
}

func (s *Server) addPullRequestReviewReactionBestEffort(repo string, pullNumber int, reviewID int64, reaction string) {
	if reviewID <= 0 || pullNumber <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddPullRequestReviewReaction(ctx, repo, pullNumber, reviewID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review %d on %s#%d: %v", reaction, reviewID, repo, pullNumber, err)
	}
}

func (s *Server) addPullRequestReviewCommentReactionBestEffort(repo string, commentID int64, reaction string) {
	if commentID <= 0 || strings.TrimSpace(repo) == "" {
		return
	}
	if strings.TrimSpace(s.Config.GitHubToken) == "" || s.GitHub == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := s.GitHub.AddPullRequestReviewCommentReaction(ctx, repo, commentID, reaction); err != nil {
		log.Printf("failed to add %q reaction for PR review comment %d in %s: %v", reaction, commentID, repo, err)
	}
}
