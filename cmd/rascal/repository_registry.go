package main

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

type repositoryRecord struct {
	FullName             string    `json:"full_name"`
	WebhookKey           string    `json:"webhook_key"`
	WebhookSecret        string    `json:"webhook_secret,omitempty"`
	Enabled              bool      `json:"enabled"`
	AgentBackend         string    `json:"agent_backend"`
	AgentSessionMode     string    `json:"agent_session_mode"`
	BaseBranchOverride   string    `json:"base_branch_override"`
	MaxConcurrentRuns    int       `json:"max_concurrent_runs"`
	AllowManual          bool      `json:"allow_manual"`
	AllowIssueLabel      bool      `json:"allow_issue_label"`
	AllowIssueEdit       bool      `json:"allow_issue_edit"`
	AllowPRComment       bool      `json:"allow_pr_comment"`
	AllowPRReview        bool      `json:"allow_pr_review"`
	AllowPRReviewComment bool      `json:"allow_pr_review_comment"`
	CreatedAt            time.Time `json:"created_at"`
	UpdatedAt            time.Time `json:"updated_at"`
}

type repositoryRoleRecord struct {
	RepoFullName string    `json:"repo_full_name"`
	UserID       string    `json:"user_id"`
	Role         string    `json:"role"`
	CreatedAt    time.Time `json:"created_at"`
	UpdatedAt    time.Time `json:"updated_at"`
}

func (a *app) newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Manage daemon repository registry and policies",
		Long:  "Create/update/list registered repositories, manage repo roles, and sync scoped GitHub webhook setup.",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newRepoAddCmd())
	cmd.AddCommand(a.newRepoUpdateCmd())
	cmd.AddCommand(a.newRepoRemoveCmd())
	cmd.AddCommand(a.newRepoListCmd())
	cmd.AddCommand(a.newRepoGetCmd())
	cmd.AddCommand(a.newRepoGrantCmd())
	cmd.AddCommand(a.newRepoRevokeCmd())
	cmd.AddCommand(a.newRepoSyncGitHubCmd())
	return cmd
}

func (a *app) newRepoAddCmd() *cobra.Command {
	var (
		githubToken          string
		webhookSecret        string
		agentBackend         string
		agentSessionMode     string
		baseBranchOverride   string
		maxConcurrentRuns    int
		allowManual          bool
		allowIssueLabel      bool
		allowIssueEdit       bool
		allowPRComment       bool
		allowPRReview        bool
		allowPRReviewComment bool
	)
	cmd := &cobra.Command{
		Use:   "add OWNER/REPO",
		Short: "Register a repository with token, webhook secret, and policy",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			if !isValidRepoArg(repo) {
				return &cliError{Code: exitInput, Message: "invalid repo", Hint: "expected OWNER/REPO"}
			}
			githubToken = strings.TrimSpace(githubToken)
			webhookSecret = strings.TrimSpace(webhookSecret)
			if githubToken == "" {
				return &cliError{Code: exitInput, Message: "missing github token", Hint: "use --github-token"}
			}
			if webhookSecret == "" {
				return &cliError{Code: exitInput, Message: "missing webhook secret", Hint: "use --webhook-secret"}
			}
			payload := map[string]any{
				"full_name":               repo,
				"github_token":            githubToken,
				"webhook_secret":          webhookSecret,
				"agent_backend":           strings.TrimSpace(agentBackend),
				"agent_session_mode":      strings.TrimSpace(agentSessionMode),
				"base_branch_override":    strings.TrimSpace(baseBranchOverride),
				"max_concurrent_runs":     maxConcurrentRuns,
				"allow_manual":            allowManual,
				"allow_issue_label":       allowIssueLabel,
				"allow_issue_edit":        allowIssueEdit,
				"allow_pr_comment":        allowPRComment,
				"allow_pr_review":         allowPRReview,
				"allow_pr_review_comment": allowPRReviewComment,
			}
			repoRecord, err := a.createRepository(payload)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"repository": repoRecord}, func() error {
				a.println("repository added: %s", repoRecord.FullName)
				a.println("webhook_key: %s", repoRecord.WebhookKey)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "repo runtime GitHub token (required)")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "repo webhook secret (required)")
	cmd.Flags().StringVar(&agentBackend, "agent-backend", "", "repo backend override: codex|goose (empty=daemon default)")
	cmd.Flags().StringVar(&agentSessionMode, "agent-session-mode", "", "repo session mode override: off|pr-only|all (empty=daemon default)")
	cmd.Flags().StringVar(&baseBranchOverride, "base-branch-override", "", "repo base branch default override")
	cmd.Flags().IntVar(&maxConcurrentRuns, "max-concurrent-runs", 0, "repo active-run cap (0=no repo cap)")
	cmd.Flags().BoolVar(&allowManual, "allow-manual", true, "allow manual/API triggers")
	cmd.Flags().BoolVar(&allowIssueLabel, "allow-issue-label", true, "allow issue label webhook trigger")
	cmd.Flags().BoolVar(&allowIssueEdit, "allow-issue-edit", true, "allow issue edit webhook trigger")
	cmd.Flags().BoolVar(&allowPRComment, "allow-pr-comment", true, "allow PR comment webhook trigger")
	cmd.Flags().BoolVar(&allowPRReview, "allow-pr-review", true, "allow PR review webhook trigger")
	cmd.Flags().BoolVar(&allowPRReviewComment, "allow-pr-review-comment", true, "allow PR review comment webhook trigger")
	return cmd
}

func (a *app) newRepoUpdateCmd() *cobra.Command {
	var (
		enabled              bool
		githubToken          string
		webhookSecret        string
		agentBackend         string
		agentSessionMode     string
		baseBranchOverride   string
		maxConcurrentRuns    int
		allowManual          bool
		allowIssueLabel      bool
		allowIssueEdit       bool
		allowPRComment       bool
		allowPRReview        bool
		allowPRReviewComment bool
	)
	cmd := &cobra.Command{
		Use:   "update OWNER/REPO",
		Short: "Patch repository policy and secrets",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			if !isValidRepoArg(repo) {
				return &cliError{Code: exitInput, Message: "invalid repo", Hint: "expected OWNER/REPO"}
			}
			payload := map[string]any{}
			if cmd.Flags().Changed("enabled") {
				payload["enabled"] = enabled
			}
			if cmd.Flags().Changed("github-token") {
				payload["github_token"] = strings.TrimSpace(githubToken)
			}
			if cmd.Flags().Changed("webhook-secret") {
				payload["webhook_secret"] = strings.TrimSpace(webhookSecret)
			}
			if cmd.Flags().Changed("agent-backend") {
				payload["agent_backend"] = strings.TrimSpace(agentBackend)
			}
			if cmd.Flags().Changed("agent-session-mode") {
				payload["agent_session_mode"] = strings.TrimSpace(agentSessionMode)
			}
			if cmd.Flags().Changed("base-branch-override") {
				payload["base_branch_override"] = strings.TrimSpace(baseBranchOverride)
			}
			if cmd.Flags().Changed("max-concurrent-runs") {
				payload["max_concurrent_runs"] = maxConcurrentRuns
			}
			if cmd.Flags().Changed("allow-manual") {
				payload["allow_manual"] = allowManual
			}
			if cmd.Flags().Changed("allow-issue-label") {
				payload["allow_issue_label"] = allowIssueLabel
			}
			if cmd.Flags().Changed("allow-issue-edit") {
				payload["allow_issue_edit"] = allowIssueEdit
			}
			if cmd.Flags().Changed("allow-pr-comment") {
				payload["allow_pr_comment"] = allowPRComment
			}
			if cmd.Flags().Changed("allow-pr-review") {
				payload["allow_pr_review"] = allowPRReview
			}
			if cmd.Flags().Changed("allow-pr-review-comment") {
				payload["allow_pr_review_comment"] = allowPRReviewComment
			}
			if len(payload) == 0 {
				return &cliError{Code: exitInput, Message: "no fields changed", Hint: "pass one or more policy flags"}
			}
			record, err := a.patchRepository(repo, payload)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"repository": record}, func() error {
				a.println("repository updated: %s", record.FullName)
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&enabled, "enabled", true, "enable or disable repository admissions")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "replace repo runtime GitHub token")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "replace repo webhook secret")
	cmd.Flags().StringVar(&agentBackend, "agent-backend", "", "repo backend override: codex|goose (empty clears override)")
	cmd.Flags().StringVar(&agentSessionMode, "agent-session-mode", "", "repo session mode override: off|pr-only|all (empty clears override)")
	cmd.Flags().StringVar(&baseBranchOverride, "base-branch-override", "", "repo base branch default override (empty clears)")
	cmd.Flags().IntVar(&maxConcurrentRuns, "max-concurrent-runs", 0, "repo active-run cap (0=no repo cap)")
	cmd.Flags().BoolVar(&allowManual, "allow-manual", true, "allow manual/API triggers")
	cmd.Flags().BoolVar(&allowIssueLabel, "allow-issue-label", true, "allow issue label webhook trigger")
	cmd.Flags().BoolVar(&allowIssueEdit, "allow-issue-edit", true, "allow issue edit webhook trigger")
	cmd.Flags().BoolVar(&allowPRComment, "allow-pr-comment", true, "allow PR comment webhook trigger")
	cmd.Flags().BoolVar(&allowPRReview, "allow-pr-review", true, "allow PR review webhook trigger")
	cmd.Flags().BoolVar(&allowPRReviewComment, "allow-pr-review-comment", true, "allow PR review comment webhook trigger")
	return cmd
}

func (a *app) newRepoRemoveCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "remove OWNER/REPO",
		Short: "Delete a repository record",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			resp, err := a.client.do(http.MethodDelete, repositoryPath(repo), nil)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer closeWithLog("close remove repository response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			return a.emit(map[string]any{"deleted": true, "repo": repo}, func() error {
				a.println("repository removed: %s", repo)
				return nil
			})
		},
	}
	return cmd
}

func (a *app) newRepoListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List registered repositories",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			resp, err := a.client.do(http.MethodGet, "/v1/repositories", nil)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer closeWithLog("close list repositories response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out struct {
				Repositories []repositoryRecord `json:"repositories"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(map[string]any{"repositories": out.Repositories}, func() error {
				if len(out.Repositories) == 0 {
					a.println("no repositories registered")
					return nil
				}
				for _, repo := range out.Repositories {
					a.println("%s enabled=%t webhook_key=%s max_concurrent_runs=%d", repo.FullName, repo.Enabled, repo.WebhookKey, repo.MaxConcurrentRuns)
				}
				return nil
			})
		},
	}
	return cmd
}

func (a *app) newRepoGetCmd() *cobra.Command {
	var includeSecret bool
	cmd := &cobra.Command{
		Use:   "get OWNER/REPO",
		Short: "Show a repository record",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo, err := a.getRepository(strings.TrimSpace(args[0]), includeSecret)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"repository": repo}, func() error {
				a.println("repository: %s", repo.FullName)
				a.println("enabled: %t", repo.Enabled)
				a.println("webhook_key: %s", repo.WebhookKey)
				if includeSecret {
					a.println("webhook_secret: %s", maskSecret(repo.WebhookSecret))
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&includeSecret, "include-secret", false, "include decrypted webhook_secret in output (requires repo/global admin)")
	return cmd
}

func (a *app) newRepoGrantCmd() *cobra.Command {
	var (
		userID string
		role   string
	)
	cmd := &cobra.Command{
		Use:   "grant OWNER/REPO --user USER_ID --role admin|trigger",
		Short: "Grant a repository role to a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			userID = strings.TrimSpace(userID)
			role = strings.ToLower(strings.TrimSpace(role))
			if userID == "" {
				return &cliError{Code: exitInput, Message: "--user is required"}
			}
			if role != "admin" && role != "trigger" {
				return &cliError{Code: exitInput, Message: "invalid role", Hint: "use admin or trigger"}
			}
			path := repositoryPath(repo) + "/roles/" + url.PathEscape(userID)
			resp, err := a.client.doJSON(http.MethodPut, path, map[string]any{"role": role})
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer closeWithLog("close grant repository role response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out struct {
				Role repositoryRoleRecord `json:"role"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(map[string]any{"role": out.Role}, func() error {
				a.println("granted %s on %s to %s", out.Role.Role, repo, userID)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&userID, "user", "", "user id to grant")
	cmd.Flags().StringVar(&role, "role", "trigger", "repository role: admin|trigger")
	return cmd
}

func (a *app) newRepoRevokeCmd() *cobra.Command {
	var userID string
	cmd := &cobra.Command{
		Use:   "revoke OWNER/REPO --user USER_ID",
		Short: "Revoke a repository role from a user",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			userID = strings.TrimSpace(userID)
			if userID == "" {
				return &cliError{Code: exitInput, Message: "--user is required"}
			}
			path := repositoryPath(repo) + "/roles/" + url.PathEscape(userID)
			resp, err := a.client.do(http.MethodDelete, path, nil)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer closeWithLog("close revoke repository role response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			return a.emit(map[string]any{"deleted": true}, func() error {
				a.println("revoked role on %s from %s", repo, userID)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&userID, "user", "", "user id to revoke")
	return cmd
}

func (a *app) newRepoSyncGitHubCmd() *cobra.Command {
	var githubAdminToken string
	cmd := &cobra.Command{
		Use:   "sync-github OWNER/REPO --github-admin-token ...",
		Short: "Ensure label and scoped webhook are up-to-date on GitHub",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := strings.TrimSpace(args[0])
			adminToken := firstNonEmpty(strings.TrimSpace(githubAdminToken), strings.TrimSpace(os.Getenv("GITHUB_ADMIN_TOKEN")), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
			if adminToken == "" {
				return &cliError{Code: exitInput, Message: "missing GitHub admin token", Hint: "use --github-admin-token or GITHUB_ADMIN_TOKEN"}
			}
			_, result, err := a.syncRepositoryGitHub(repo, adminToken)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{
				"repo":        repo,
				"synced":      true,
				"webhook_url": result.WebhookURL,
			}, func() error {
				a.println("GitHub sync complete for %s", repo)
				a.println("webhook: %s", result.WebhookURL)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubAdminToken, "github-admin-token", "", "GitHub token with repo webhook + issue permissions")
	return cmd
}

func (a *app) syncRepositoryGitHub(repo, githubAdminToken string) (repositoryRecord, repoEnableResult, error) {
	record, err := a.getRepository(repo, true)
	if err != nil {
		return repositoryRecord{}, repoEnableResult{}, err
	}
	if strings.TrimSpace(record.WebhookKey) == "" {
		return repositoryRecord{}, repoEnableResult{}, &cliError{Code: exitRuntime, Message: "repository missing webhook_key"}
	}
	if strings.TrimSpace(record.WebhookSecret) == "" {
		return repositoryRecord{}, repoEnableResult{}, &cliError{Code: exitRuntime, Message: "repository missing webhook_secret"}
	}
	webhookURL := strings.TrimRight(a.cfg.ServerURL, "/") + "/v1/webhooks/github/" + record.WebhookKey
	result, err := a.runRepoEnable(repoEnableInput{
		Repo:          repo,
		GitHubToken:   githubAdminToken,
		WebhookSecret: record.WebhookSecret,
		WebhookURL:    webhookURL,
		Timeout:       45 * time.Second,
	})
	if err != nil {
		return repositoryRecord{}, repoEnableResult{}, err
	}
	return record, result, nil
}

func (a *app) createRepository(payload map[string]any) (repositoryRecord, error) {
	resp, err := a.client.doJSON(http.MethodPost, "/v1/repositories", payload)
	if err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close create repository response body", resp.Body)
	if resp.StatusCode >= 300 {
		return repositoryRecord{}, decodeServerError(resp)
	}
	var out struct {
		Repository repositoryRecord `json:"repository"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Repository, nil
}

func (a *app) patchRepository(repo string, payload map[string]any) (repositoryRecord, error) {
	resp, err := a.client.doJSON(http.MethodPatch, repositoryPath(repo), payload)
	if err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close patch repository response body", resp.Body)
	if resp.StatusCode >= 300 {
		return repositoryRecord{}, decodeServerError(resp)
	}
	var out struct {
		Repository repositoryRecord `json:"repository"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Repository, nil
}

func (a *app) getRepository(repo string, includeSecret bool) (repositoryRecord, error) {
	path := repositoryPath(repo)
	if includeSecret {
		path += "?include_secret=1"
	}
	resp, err := a.client.do(http.MethodGet, path, nil)
	if err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close get repository response body", resp.Body)
	if resp.StatusCode >= 300 {
		return repositoryRecord{}, decodeServerError(resp)
	}
	var out struct {
		Repository repositoryRecord `json:"repository"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return repositoryRecord{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Repository, nil
}

func repositoryPath(repo string) string {
	owner, name := splitRepoArg(strings.TrimSpace(repo))
	return "/v1/repositories/" + url.PathEscape(owner) + "/" + url.PathEscape(name)
}

func isValidRepoArg(repo string) bool {
	owner, name := splitRepoArg(repo)
	return owner != "" && name != ""
}

func splitRepoArg(repo string) (string, string) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 {
		return "", ""
	}
	return strings.TrimSpace(parts[0]), strings.TrimSpace(parts[1])
}
