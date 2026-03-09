package main

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/rtzll/rascal/internal/agent"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/repositories"
	"github.com/rtzll/rascal/internal/state"
)

type createRepositoryRequest struct {
	FullName             string `json:"full_name"`
	GitHubToken          string `json:"github_token"`
	WebhookSecret        string `json:"webhook_secret"`
	AgentBackend         string `json:"agent_backend"`
	AgentSessionMode     string `json:"agent_session_mode"`
	BaseBranchOverride   string `json:"base_branch_override"`
	MaxConcurrentRuns    *int   `json:"max_concurrent_runs"`
	AllowManual          *bool  `json:"allow_manual"`
	AllowIssueLabel      *bool  `json:"allow_issue_label"`
	AllowIssueEdit       *bool  `json:"allow_issue_edit"`
	AllowPRComment       *bool  `json:"allow_pr_comment"`
	AllowPRReview        *bool  `json:"allow_pr_review"`
	AllowPRReviewComment *bool  `json:"allow_pr_review_comment"`
}

type patchRepositoryRequest struct {
	Enabled              *bool   `json:"enabled"`
	GitHubToken          *string `json:"github_token"`
	WebhookSecret        *string `json:"webhook_secret"`
	AgentBackend         *string `json:"agent_backend"`
	AgentSessionMode     *string `json:"agent_session_mode"`
	BaseBranchOverride   *string `json:"base_branch_override"`
	MaxConcurrentRuns    *int    `json:"max_concurrent_runs"`
	AllowManual          *bool   `json:"allow_manual"`
	AllowIssueLabel      *bool   `json:"allow_issue_label"`
	AllowIssueEdit       *bool   `json:"allow_issue_edit"`
	AllowPRComment       *bool   `json:"allow_pr_comment"`
	AllowPRReview        *bool   `json:"allow_pr_review"`
	AllowPRReviewComment *bool   `json:"allow_pr_review_comment"`
}

type upsertRepositoryRoleRequest struct {
	Role string `json:"role"`
}

type manualAdmissionResult struct {
	Resolved *repositories.ResolvedRepoConfig
	Legacy   bool
}

func (s *server) handleRepositories(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		if !requesterIsAdmin(r.Context()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		repos, err := s.store.ListRepositories()
		if err != nil {
			http.Error(w, "failed to list repositories", http.StatusInternalServerError)
			return
		}
		out := make([]map[string]any, 0, len(repos))
		for _, repo := range repos {
			out = append(out, repositoryResponse(repo, nil))
		}
		writeJSON(w, http.StatusOK, map[string]any{"repositories": out})
	case http.MethodPost:
		if !requesterIsAdmin(r.Context()) {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}

		var req createRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		req.FullName = strings.TrimSpace(req.FullName)
		req.GitHubToken = strings.TrimSpace(req.GitHubToken)
		req.WebhookSecret = strings.TrimSpace(req.WebhookSecret)
		req.AgentBackend = strings.TrimSpace(req.AgentBackend)
		req.AgentSessionMode = strings.TrimSpace(req.AgentSessionMode)
		req.BaseBranchOverride = strings.TrimSpace(req.BaseBranchOverride)

		if !isValidRepoFullName(req.FullName) {
			http.Error(w, "invalid full_name (expected owner/repo)", http.StatusBadRequest)
			return
		}
		if req.GitHubToken == "" {
			http.Error(w, "github_token is required", http.StatusBadRequest)
			return
		}
		if req.WebhookSecret == "" {
			http.Error(w, "webhook_secret is required", http.StatusBadRequest)
			return
		}
		if !isValidAgentBackendOverride(req.AgentBackend) {
			http.Error(w, "invalid agent_backend", http.StatusBadRequest)
			return
		}
		if !isValidSessionModeOverride(req.AgentSessionMode) {
			http.Error(w, "invalid agent_session_mode", http.StatusBadRequest)
			return
		}

		maxConcurrent := 0
		if req.MaxConcurrentRuns != nil {
			maxConcurrent = *req.MaxConcurrentRuns
		}
		if maxConcurrent < 0 {
			http.Error(w, "max_concurrent_runs cannot be negative", http.StatusBadRequest)
			return
		}

		encryptedToken, err := s.cipher.Encrypt([]byte(req.GitHubToken))
		if err != nil {
			http.Error(w, "failed to encrypt github token", http.StatusInternalServerError)
			return
		}
		encryptedSecret, err := s.cipher.Encrypt([]byte(req.WebhookSecret))
		if err != nil {
			http.Error(w, "failed to encrypt webhook secret", http.StatusInternalServerError)
			return
		}

		var created state.Repository
		for attempt := 0; attempt < 5; attempt++ {
			webhookKey, genErr := newWebhookKey()
			if genErr != nil {
				http.Error(w, "failed to generate webhook key", http.StatusInternalServerError)
				return
			}
			created, err = s.store.CreateRepository(state.CreateRepositoryInput{
				FullName:               req.FullName,
				WebhookKey:             webhookKey,
				Enabled:                true,
				EncryptedGitHubToken:   encryptedToken,
				EncryptedWebhookSecret: encryptedSecret,
				AgentBackend:           req.AgentBackend,
				AgentSessionMode:       req.AgentSessionMode,
				BaseBranchOverride:     req.BaseBranchOverride,
				MaxConcurrentRuns:      maxConcurrent,
				AllowManual:            boolOrDefault(req.AllowManual, true),
				AllowIssueLabel:        boolOrDefault(req.AllowIssueLabel, true),
				AllowIssueEdit:         boolOrDefault(req.AllowIssueEdit, true),
				AllowPRComment:         boolOrDefault(req.AllowPRComment, true),
				AllowPRReview:          boolOrDefault(req.AllowPRReview, true),
				AllowPRReviewComment:   boolOrDefault(req.AllowPRReviewComment, true),
			})
			if err == nil {
				writeJSON(w, http.StatusCreated, map[string]any{"repository": repositoryResponse(created, nil)})
				return
			}
			if strings.Contains(err.Error(), "UNIQUE constraint failed: repositories.webhook_key") {
				continue
			}
			if strings.Contains(err.Error(), "UNIQUE constraint failed: repositories.full_name") {
				http.Error(w, "repository already exists", http.StatusConflict)
				return
			}
			http.Error(w, "failed to create repository: "+err.Error(), http.StatusInternalServerError)
			return
		}
		http.Error(w, "failed to create repository (webhook key collision)", http.StatusInternalServerError)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleRepositorySubresources(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/repositories/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	if len(parts) < 2 {
		http.Error(w, "repository is required", http.StatusBadRequest)
		return
	}
	owner, err := url.PathUnescape(parts[0])
	if err != nil {
		http.Error(w, "invalid owner", http.StatusBadRequest)
		return
	}
	name, err := url.PathUnescape(parts[1])
	if err != nil {
		http.Error(w, "invalid repo", http.StatusBadRequest)
		return
	}
	fullName := strings.TrimSpace(owner + "/" + name)
	if !isValidRepoFullName(fullName) {
		http.Error(w, "invalid repository", http.StatusBadRequest)
		return
	}
	rest := parts[2:]
	if len(rest) == 0 {
		s.handleRepositoryResource(w, r, fullName)
		return
	}
	if rest[0] != "roles" {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	switch len(rest) {
	case 1:
		s.handleRepositoryRolesCollection(w, r, fullName)
	case 2:
		userID, err := url.PathUnescape(rest[1])
		if err != nil {
			http.Error(w, "invalid user id", http.StatusBadRequest)
			return
		}
		s.handleRepositoryRoleResource(w, r, fullName, userID)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (s *server) handleRepositoryResource(w http.ResponseWriter, r *http.Request, fullName string) {
	switch r.Method {
	case http.MethodGet:
		allowed, err := s.userCanViewRepo(r.Context(), fullName)
		if err != nil {
			http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
			return
		}
		if !allowed {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		repo, ok, err := s.store.GetRepository(fullName)
		if err != nil {
			http.Error(w, "failed to load repository", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		var secret *string
		if raw := strings.TrimSpace(r.URL.Query().Get("include_secret")); raw != "" && raw != "0" && !strings.EqualFold(raw, "false") {
			canManage, manageErr := s.userCanManageRepo(r.Context(), fullName)
			if manageErr != nil {
				http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
				return
			}
			if !canManage {
				http.Error(w, "forbidden", http.StatusForbidden)
				return
			}
			plain, decErr := s.cipher.Decrypt(repo.EncryptedWebhookSecret)
			if decErr != nil {
				http.Error(w, "failed to decrypt repository secret", http.StatusInternalServerError)
				return
			}
			value := strings.TrimSpace(string(plain))
			secret = &value
		}
		writeJSON(w, http.StatusOK, map[string]any{"repository": repositoryResponse(repo, secret)})
	case http.MethodPatch:
		canManage, err := s.userCanManageRepo(r.Context(), fullName)
		if err != nil {
			http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
			return
		}
		if !canManage {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		repo, ok, err := s.store.GetRepository(fullName)
		if err != nil {
			http.Error(w, "failed to load repository", http.StatusInternalServerError)
			return
		}
		if !ok {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		var req patchRepositoryRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}

		updated := state.UpdateRepositoryInput{
			FullName:               repo.FullName,
			WebhookKey:             repo.WebhookKey,
			Enabled:                repo.Enabled,
			EncryptedGitHubToken:   repo.EncryptedGitHubToken,
			EncryptedWebhookSecret: repo.EncryptedWebhookSecret,
			AgentBackend:           repo.AgentBackend,
			AgentSessionMode:       repo.AgentSessionMode,
			BaseBranchOverride:     repo.BaseBranchOverride,
			MaxConcurrentRuns:      repo.MaxConcurrentRuns,
			AllowManual:            repo.AllowManual,
			AllowIssueLabel:        repo.AllowIssueLabel,
			AllowIssueEdit:         repo.AllowIssueEdit,
			AllowPRComment:         repo.AllowPRComment,
			AllowPRReview:          repo.AllowPRReview,
			AllowPRReviewComment:   repo.AllowPRReviewComment,
		}
		if req.Enabled != nil {
			updated.Enabled = *req.Enabled
		}
		if req.GitHubToken != nil {
			value := strings.TrimSpace(*req.GitHubToken)
			if value == "" {
				http.Error(w, "github_token cannot be empty when provided", http.StatusBadRequest)
				return
			}
			encrypted, encErr := s.cipher.Encrypt([]byte(value))
			if encErr != nil {
				http.Error(w, "failed to encrypt github token", http.StatusInternalServerError)
				return
			}
			updated.EncryptedGitHubToken = encrypted
		}
		if req.WebhookSecret != nil {
			value := strings.TrimSpace(*req.WebhookSecret)
			if value == "" {
				http.Error(w, "webhook_secret cannot be empty when provided", http.StatusBadRequest)
				return
			}
			encrypted, encErr := s.cipher.Encrypt([]byte(value))
			if encErr != nil {
				http.Error(w, "failed to encrypt webhook secret", http.StatusInternalServerError)
				return
			}
			updated.EncryptedWebhookSecret = encrypted
		}
		if req.AgentBackend != nil {
			value := strings.TrimSpace(*req.AgentBackend)
			if !isValidAgentBackendOverride(value) {
				http.Error(w, "invalid agent_backend", http.StatusBadRequest)
				return
			}
			updated.AgentBackend = value
		}
		if req.AgentSessionMode != nil {
			value := strings.TrimSpace(*req.AgentSessionMode)
			if !isValidSessionModeOverride(value) {
				http.Error(w, "invalid agent_session_mode", http.StatusBadRequest)
				return
			}
			updated.AgentSessionMode = value
		}
		if req.BaseBranchOverride != nil {
			updated.BaseBranchOverride = strings.TrimSpace(*req.BaseBranchOverride)
		}
		if req.MaxConcurrentRuns != nil {
			if *req.MaxConcurrentRuns < 0 {
				http.Error(w, "max_concurrent_runs cannot be negative", http.StatusBadRequest)
				return
			}
			updated.MaxConcurrentRuns = *req.MaxConcurrentRuns
		}
		if req.AllowManual != nil {
			updated.AllowManual = *req.AllowManual
		}
		if req.AllowIssueLabel != nil {
			updated.AllowIssueLabel = *req.AllowIssueLabel
		}
		if req.AllowIssueEdit != nil {
			updated.AllowIssueEdit = *req.AllowIssueEdit
		}
		if req.AllowPRComment != nil {
			updated.AllowPRComment = *req.AllowPRComment
		}
		if req.AllowPRReview != nil {
			updated.AllowPRReview = *req.AllowPRReview
		}
		if req.AllowPRReviewComment != nil {
			updated.AllowPRReviewComment = *req.AllowPRReviewComment
		}

		repo, err = s.store.UpdateRepository(updated)
		if err != nil {
			http.Error(w, "failed to update repository: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"repository": repositoryResponse(repo, nil)})
	case http.MethodDelete:
		canManage, err := s.userCanManageRepo(r.Context(), fullName)
		if err != nil {
			http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
			return
		}
		if !canManage {
			http.Error(w, "forbidden", http.StatusForbidden)
			return
		}
		if err := s.store.DeleteRepository(fullName); err != nil {
			http.Error(w, "failed to delete repository", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleRepositoryRolesCollection(w http.ResponseWriter, r *http.Request, fullName string) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	canManage, err := s.userCanManageRepo(r.Context(), fullName)
	if err != nil {
		http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
		return
	}
	if !canManage {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	roles, err := s.store.ListRepositoryUserRoles(fullName)
	if err != nil {
		http.Error(w, "failed to list repository roles", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"roles": roles})
}

func (s *server) handleRepositoryRoleResource(w http.ResponseWriter, r *http.Request, fullName, userID string) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		http.Error(w, "user id is required", http.StatusBadRequest)
		return
	}
	canManage, err := s.userCanManageRepo(r.Context(), fullName)
	if err != nil {
		http.Error(w, "failed to load repository ACL", http.StatusInternalServerError)
		return
	}
	if !canManage {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}

	switch r.Method {
	case http.MethodPut:
		var req upsertRepositoryRoleRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		role := strings.ToLower(strings.TrimSpace(req.Role))
		if role != string(state.RepositoryRoleAdmin) && role != string(state.RepositoryRoleTrigger) {
			http.Error(w, "invalid role", http.StatusBadRequest)
			return
		}
		assigned, err := s.store.UpsertRepositoryUserRole(state.UpsertRepositoryUserRoleInput{
			RepoFullName: fullName,
			UserID:       userID,
			Role:         state.RepositoryRole(role),
		})
		if err != nil {
			if strings.Contains(err.Error(), "FOREIGN KEY constraint failed") {
				http.Error(w, "user or repository not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to upsert repository role: "+err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"role": assigned})
	case http.MethodDelete:
		if err := s.store.DeleteRepositoryUserRole(fullName, userID); err != nil {
			http.Error(w, "failed to delete repository role", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"deleted": true})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) userCanViewRepo(ctx context.Context, fullName string) (bool, error) {
	if requesterIsAdmin(ctx) {
		return true, nil
	}
	role, ok, err := s.store.GetRepositoryUserRole(fullName, requesterUserID(ctx))
	if err != nil {
		return false, fmt.Errorf("get repository role for viewer: %w", err)
	}
	if !ok {
		return false, nil
	}
	switch role.Role {
	case state.RepositoryRoleAdmin, state.RepositoryRoleTrigger:
		return true, nil
	default:
		return false, nil
	}
}

func (s *server) userCanManageRepo(ctx context.Context, fullName string) (bool, error) {
	if requesterIsAdmin(ctx) {
		return true, nil
	}
	role, ok, err := s.store.GetRepositoryUserRole(fullName, requesterUserID(ctx))
	if err != nil {
		return false, fmt.Errorf("get repository role for manager: %w", err)
	}
	return ok && role.Role == state.RepositoryRoleAdmin, nil
}

func (s *server) userCanTriggerRepo(ctx context.Context, fullName string) (bool, error) {
	if requesterIsAdmin(ctx) {
		return true, nil
	}
	role, ok, err := s.store.GetRepositoryUserRole(fullName, requesterUserID(ctx))
	if err != nil {
		return false, fmt.Errorf("get repository role for trigger: %w", err)
	}
	if !ok {
		return false, nil
	}
	return role.Role == state.RepositoryRoleAdmin || role.Role == state.RepositoryRoleTrigger, nil
}

func (s *server) legacyRepoFallbackAllowed() (bool, error) {
	count, err := s.store.CountRepositories()
	if err != nil {
		return false, fmt.Errorf("count repositories: %w", err)
	}
	return count == 0, nil
}

func (s *server) admitManualRun(ctx context.Context, repo string) (manualAdmissionResult, int, string, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" {
		return manualAdmissionResult{}, http.StatusBadRequest, "repo is required", nil
	}
	if !isValidRepoFullName(repo) {
		return manualAdmissionResult{}, http.StatusBadRequest, "invalid repo (expected owner/repo)", nil
	}
	if s.repo == nil {
		legacyAllowed, err := s.legacyRepoFallbackAllowed()
		if err != nil {
			return manualAdmissionResult{}, http.StatusInternalServerError, "failed to load repository policy", err
		}
		if legacyAllowed {
			return manualAdmissionResult{Legacy: true}, 0, "", nil
		}
		return manualAdmissionResult{}, http.StatusNotFound, "repository not found", nil
	}

	resolved, err := s.repo.Resolve(repo)
	if err != nil {
		if errors.Is(err, repositories.ErrNotFound) {
			legacyAllowed, fallbackErr := s.legacyRepoFallbackAllowed()
			if fallbackErr != nil {
				return manualAdmissionResult{}, http.StatusInternalServerError, "failed to load repository policy", fallbackErr
			}
			if legacyAllowed {
				return manualAdmissionResult{Legacy: true}, 0, "", nil
			}
			return manualAdmissionResult{}, http.StatusNotFound, "repository not found", nil
		}
		return manualAdmissionResult{}, http.StatusInternalServerError, "failed to resolve repository", fmt.Errorf("resolve repository %q: %w", repo, err)
	}
	if !resolved.Enabled {
		return manualAdmissionResult{}, http.StatusForbidden, "repository is disabled", nil
	}
	if !resolved.AllowManual {
		return manualAdmissionResult{}, http.StatusForbidden, "manual triggers are disabled for repository", nil
	}
	allowed, err := s.userCanTriggerRepo(ctx, resolved.FullName)
	if err != nil {
		return manualAdmissionResult{}, http.StatusInternalServerError, "failed to load repository ACL", err
	}
	if !allowed {
		return manualAdmissionResult{}, http.StatusForbidden, "missing repository trigger permission", nil
	}
	return manualAdmissionResult{Resolved: &resolved}, 0, "", nil
}

func (s *server) githubClientForRepo(repo string) (githubClient, error) {
	repo = strings.TrimSpace(repo)
	if repo != "" && s.ghByRepo != nil {
		client, err := s.ghByRepo.ForRepo(repo)
		if err == nil {
			return client, nil
		}
		if !errors.Is(err, repositories.ErrNotFound) {
			return nil, fmt.Errorf("resolve github client for repo %q: %w", repo, err)
		}
		legacyAllowed, fallbackErr := s.legacyRepoFallbackAllowed()
		if fallbackErr != nil {
			return nil, fallbackErr
		}
		if !legacyAllowed {
			return nil, fmt.Errorf("resolve github client for repo %q: %w", repo, err)
		}
	}
	if s.gh != nil && strings.TrimSpace(s.cfg.GitHubToken) != "" {
		return s.gh, nil
	}
	return nil, repositories.ErrNotFound
}

func repositoryResponse(repo state.Repository, webhookSecret *string) map[string]any {
	out := map[string]any{
		"full_name":               repo.FullName,
		"webhook_key":             repo.WebhookKey,
		"enabled":                 repo.Enabled,
		"agent_backend":           repo.AgentBackend,
		"agent_session_mode":      repo.AgentSessionMode,
		"base_branch_override":    repo.BaseBranchOverride,
		"max_concurrent_runs":     repo.MaxConcurrentRuns,
		"allow_manual":            repo.AllowManual,
		"allow_issue_label":       repo.AllowIssueLabel,
		"allow_issue_edit":        repo.AllowIssueEdit,
		"allow_pr_comment":        repo.AllowPRComment,
		"allow_pr_review":         repo.AllowPRReview,
		"allow_pr_review_comment": repo.AllowPRReviewComment,
		"created_at":              repo.CreatedAt,
		"updated_at":              repo.UpdatedAt,
	}
	if webhookSecret != nil {
		out["webhook_secret"] = *webhookSecret
	}
	return out
}

func newWebhookKey() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate webhook key: %w", err)
	}
	return hex.EncodeToString(buf), nil
}

func boolOrDefault(v *bool, fallback bool) bool {
	if v == nil {
		return fallback
	}
	return *v
}

func isValidRepoFullName(fullName string) bool {
	fullName = strings.TrimSpace(fullName)
	if strings.Contains(fullName, " ") {
		return false
	}
	parts := strings.Split(fullName, "/")
	if len(parts) != 2 {
		return false
	}
	return strings.TrimSpace(parts[0]) != "" && strings.TrimSpace(parts[1]) != ""
}

func isValidAgentBackendOverride(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "" || raw == string(agent.BackendCodex) || raw == string(agent.BackendGoose)
}

func isValidSessionModeOverride(raw string) bool {
	raw = strings.TrimSpace(raw)
	return raw == "" || raw == string(agent.SessionModeOff) || raw == string(agent.SessionModePROnly) || raw == string(agent.SessionModeAll)
}

func mapWebhookTriggerAllowed(cfg repositories.ResolvedRepoConfig, trigger string) bool {
	switch strings.TrimSpace(trigger) {
	case "issue_label":
		return cfg.AllowedWebhookTriggers.IssueLabel
	case "issue_edited":
		return cfg.AllowedWebhookTriggers.IssueEdit
	case "pr_comment":
		return cfg.AllowedWebhookTriggers.PRComment
	case "pr_review":
		return cfg.AllowedWebhookTriggers.PRReview
	case "pr_review_comment":
		return cfg.AllowedWebhookTriggers.PRReviewComment
	default:
		return true
	}
}

func (s *server) resolveRunRepoConfig(repo string) (*repositories.ResolvedRepoConfig, error) {
	repo = strings.TrimSpace(repo)
	if repo == "" || s.repo == nil {
		return nil, nil
	}
	resolved, err := s.repo.Resolve(repo)
	if err == nil {
		return &resolved, nil
	}
	if !errors.Is(err, repositories.ErrNotFound) {
		return nil, fmt.Errorf("resolve repository %q: %w", repo, err)
	}
	legacyAllowed, fallbackErr := s.legacyRepoFallbackAllowed()
	if fallbackErr != nil {
		return nil, fallbackErr
	}
	if legacyAllowed {
		return nil, nil
	}
	return nil, fmt.Errorf("resolve repository %q: %w", repo, err)
}

func payloadRepositoryFullName(payload []byte) string {
	var envelope struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
	}
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return ""
	}
	return strings.TrimSpace(envelope.Repository.FullName)
}

func (s *server) handleWebhookScoped(w http.ResponseWriter, r *http.Request) {
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

	webhookKey := strings.TrimPrefix(r.URL.Path, "/v1/webhooks/github/")
	webhookKey = strings.Trim(webhookKey, "/")
	if webhookKey == "" || strings.Contains(webhookKey, "/") {
		http.Error(w, "invalid webhook key", http.StatusBadRequest)
		return
	}
	resolved, err := s.repo.ResolveByWebhookKey(webhookKey)
	if err != nil {
		if errors.Is(err, repositories.ErrNotFound) {
			http.Error(w, "repository not found", http.StatusNotFound)
			return
		}
		http.Error(w, "failed to resolve repository", http.StatusInternalServerError)
		return
	}
	if !resolved.Enabled {
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": false, "reason": "repository disabled"})
		return
	}

	payload, deliveryClaim, ok := s.scopedWebhookDelivery(w, r, resolved.WebhookSecret)
	if !ok {
		return
	}
	if fullName := payloadRepositoryFullName(payload); fullName == "" || fullName != resolved.FullName {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "repository mismatch", http.StatusBadRequest)
		return
	}

	eventType := ghapi.EventType(r.Header)
	allowed, relevant, err := scopedWebhookTriggerAllowed(eventType, payload, resolved)
	if err != nil {
		if deliveryClaim.ID != "" {
			s.releaseDeliveryClaimBestEffort(deliveryClaim)
		}
		http.Error(w, "invalid webhook payload", http.StatusBadRequest)
		return
	}
	if relevant && !allowed {
		if deliveryClaim.ID != "" {
			if err := s.store.CompleteDeliveryClaim(deliveryClaim); err != nil {
				http.Error(w, "failed to finalize delivery id", http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, http.StatusAccepted, map[string]any{"accepted": false})
		return
	}

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

func scopedWebhookTriggerAllowed(eventType string, payload []byte, cfg repositories.ResolvedRepoConfig) (bool, bool, error) {
	switch eventType {
	case "issues":
		var ev ghapi.IssuesEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode issues event: %w", err)
		}
		switch ev.Action {
		case "labeled":
			if !strings.EqualFold(strings.TrimSpace(ev.Label.Name), "rascal") {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "issue_label"), true, nil
		case "edited":
			if !issueHasLabel(ev.Issue.Labels, "rascal") {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "issue_edited"), true, nil
		default:
			return true, false, nil
		}
	case "issue_comment":
		var ev ghapi.IssueCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode issue_comment event: %w", err)
		}
		if ev.Issue.PullRequest == nil {
			return true, false, nil
		}
		switch ev.Action {
		case "created":
			return mapWebhookTriggerAllowed(cfg, "pr_comment"), true, nil
		case "edited":
			if !issueCommentBodyChanged(ev) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "pr_comment"), true, nil
		default:
			return true, false, nil
		}
	case "pull_request_review":
		var ev ghapi.PullRequestReviewEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode pull_request_review event: %w", err)
		}
		if ev.Action != "submitted" {
			return true, false, nil
		}
		return mapWebhookTriggerAllowed(cfg, "pr_review"), true, nil
	case "pull_request_review_comment":
		var ev ghapi.PullRequestReviewCommentEvent
		if err := json.Unmarshal(payload, &ev); err != nil {
			return false, false, fmt.Errorf("decode pull_request_review_comment event: %w", err)
		}
		switch ev.Action {
		case "created":
			return mapWebhookTriggerAllowed(cfg, "pr_review_comment"), true, nil
		case "edited":
			if !reviewCommentBodyChanged(ev) {
				return true, false, nil
			}
			return mapWebhookTriggerAllowed(cfg, "pr_review_comment"), true, nil
		default:
			return true, false, nil
		}
	default:
		return true, false, nil
	}
}

func (s *server) scopedWebhookDelivery(w http.ResponseWriter, r *http.Request, webhookSecret string) ([]byte, state.DeliveryClaim, bool) {
	payload, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 2*1024*1024))
	if err != nil {
		http.Error(w, "failed to read body", http.StatusBadRequest)
		return nil, state.DeliveryClaim{}, false
	}
	sig := r.Header.Get("X-Hub-Signature-256")
	if !ghapi.VerifySignatureSHA256([]byte(webhookSecret), payload, sig) {
		http.Error(w, "invalid webhook signature", http.StatusUnauthorized)
		return nil, state.DeliveryClaim{}, false
	}
	deliveryID := ghapi.DeliveryID(r.Header)
	var claim state.DeliveryClaim
	if deliveryID != "" {
		c, claimed, claimErr := s.store.ClaimDelivery(deliveryID, s.instanceID)
		if claimErr != nil {
			http.Error(w, "failed to claim delivery id", http.StatusInternalServerError)
			return nil, state.DeliveryClaim{}, false
		}
		if !claimed {
			writeJSON(w, http.StatusOK, map[string]any{"duplicate": true})
			return nil, state.DeliveryClaim{}, false
		}
		claim = c
	}
	return payload, claim, true
}
