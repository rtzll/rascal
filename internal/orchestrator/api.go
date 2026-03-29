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
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/api"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/logs"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

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

	runReq, err := s.buildCreateTaskRunRequest(req, trigger, requesterUserID(r.Context()))
	if err != nil {
		http.Error(w, "invalid retry source: "+err.Error(), http.StatusBadRequest)
		return
	}

	run, err := s.CreateAndQueueRun(runReq)
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

func (s *Server) buildCreateTaskRunRequest(req createTaskRequest, trigger runtrigger.Name, createdByUserID string) (RunRequest, error) {
	runReq := RunRequest{
		TaskID:          req.TaskID,
		Repo:            req.Repo,
		Instruction:     req.Instruction,
		AgentRuntime:    req.AgentRuntime,
		BaseBranch:      req.BaseBranch,
		Trigger:         trigger,
		Debug:           req.Debug,
		CreatedByUserID: createdByUserID,
	}
	if trigger != runtrigger.NameRetry {
		return runReq, nil
	}
	return s.hydrateRetryRunRequest(runReq, strings.TrimSpace(req.SourceRunID))
}

func (s *Server) hydrateRetryRunRequest(runReq RunRequest, sourceRunID string) (RunRequest, error) {
	var (
		sourceRun state.Run
		ok        bool
	)
	switch {
	case sourceRunID != "":
		sourceRun, ok = s.Store.GetRun(sourceRunID)
		if !ok {
			return RunRequest{}, fmt.Errorf("run %q not found", sourceRunID)
		}
	default:
		sourceRun, ok = s.Store.LastRunForTask(runReq.TaskID)
		if !ok {
			return RunRequest{}, fmt.Errorf("no prior run found for task %q", runReq.TaskID)
		}
	}

	if strings.TrimSpace(runReq.TaskID) != "" && strings.TrimSpace(runReq.TaskID) != strings.TrimSpace(sourceRun.TaskID) {
		return RunRequest{}, fmt.Errorf("source run %q belongs to task %q, not %q", sourceRun.ID, sourceRun.TaskID, runReq.TaskID)
	}
	if strings.TrimSpace(runReq.Repo) != "" && state.NormalizeRepo(runReq.Repo) != state.NormalizeRepo(sourceRun.Repo) {
		return RunRequest{}, fmt.Errorf("source run %q belongs to repo %q, not %q", sourceRun.ID, sourceRun.Repo, runReq.Repo)
	}

	if strings.TrimSpace(runReq.TaskID) == "" {
		runReq.TaskID = sourceRun.TaskID
	}
	if strings.TrimSpace(runReq.Repo) == "" {
		runReq.Repo = sourceRun.Repo
	}
	if strings.TrimSpace(runReq.Instruction) == "" {
		runReq.Instruction = sourceRun.Instruction
	}
	if strings.TrimSpace(runReq.BaseBranch) == "" {
		runReq.BaseBranch = sourceRun.BaseBranch
	}
	if strings.TrimSpace(runReq.HeadBranch) == "" {
		runReq.HeadBranch = sourceRun.HeadBranch
	}
	if strings.TrimSpace(runReq.Context) == "" {
		runReq.Context = sourceRun.Context
	}
	if runReq.IssueNumber <= 0 {
		runReq.IssueNumber = sourceRun.IssueNumber
	}
	if runReq.PRNumber <= 0 {
		runReq.PRNumber = sourceRun.PRNumber
	}
	if runReq.PRStatus == state.PRStatusNone {
		runReq.PRStatus = sourceRun.PRStatus
	}

	if task, ok := s.Store.GetTask(runReq.TaskID); ok {
		if runReq.IssueNumber <= 0 {
			runReq.IssueNumber = task.IssueNumber
		}
		if runReq.PRNumber <= 0 {
			runReq.PRNumber = task.PRNumber
		}
	}
	if runReq.PRNumber > 0 && runReq.PRStatus == state.PRStatusNone {
		runReq.PRStatus = state.PRStatusOpen
	}

	if runReq.ResponseTarget == nil {
		target, ok, err := loadPersistedRunResponseTarget(s.Store, sourceRun)
		if err != nil {
			return RunRequest{}, fmt.Errorf("load response target for run %q: %w", sourceRun.ID, err)
		}
		if ok {
			runReq.ResponseTarget = &target
		} else {
			responseIssue := runReq.IssueNumber
			if responseIssue <= 0 {
				responseIssue = runReq.PRNumber
			}
			if responseIssue > 0 {
				runReq.ResponseTarget = &RunResponseTarget{
					Repo:        runReq.Repo,
					IssueNumber: responseIssue,
					Trigger:     sourceRun.Trigger,
				}
			}
		}
	}

	return runReq, nil
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
		AgentRuntime:    req.AgentRuntime,
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
			creds []state.Credential
			err   error
		)
		if requesterIsAdmin(r.Context()) {
			creds, err = s.Store.ListAllCredentials()
		} else {
			creds, err = s.Store.ListCredentialsByOwner(requesterUserID(r.Context()))
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
		credential, err := s.Store.CreateCredential(state.CreateCredentialInput{
			ID:                id,
			OwnerUserID:       ownerUserID,
			Scope:             scope,
			Provider:          strings.TrimSpace(req.Provider),
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
	credential, ok, err := s.Store.GetCredential(id)
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
		if err := s.Store.SetCredentialStatus(credential.ID, state.CredentialStatusDisabled, nil, "disabled by API"); err != nil {
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
		updated := state.UpdateCredentialInput{
			ID:                credential.ID,
			OwnerUserID:       credential.OwnerUserID,
			Scope:             credential.Scope,
			Provider:          credential.Provider,
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
		if req.Provider != nil {
			updated.Provider = strings.TrimSpace(*req.Provider)
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
		credential, err := s.Store.UpdateCredential(updated)
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

func (s *Server) canAccessCredential(ctx context.Context, credential state.Credential) bool {
	if requesterIsAdmin(ctx) {
		return true
	}
	return credential.Scope == state.CredentialScopePersonal && credential.OwnerUserID == requesterUserID(ctx)
}

func credentialResponse(credential state.Credential) api.Credential {
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
		updated, err := s.SM.Transition(runID, state.StatusCanceled, WithError("canceled by user"))
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
	return status == state.StatusSucceeded || status == state.StatusFailed || status == state.StatusCanceled || status == state.StatusReview
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
