package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/state/sqlitegen"
)

type RepositoryRole string

const (
	RepositoryRoleAdmin   RepositoryRole = "admin"
	RepositoryRoleTrigger RepositoryRole = "trigger"
)

type Repository struct {
	FullName               string    `json:"full_name"`
	WebhookKey             string    `json:"webhook_key"`
	Enabled                bool      `json:"enabled"`
	EncryptedGitHubToken   []byte    `json:"-"`
	EncryptedWebhookSecret []byte    `json:"-"`
	AgentBackend           string    `json:"agent_backend"`
	AgentSessionMode       string    `json:"agent_session_mode"`
	BaseBranchOverride     string    `json:"base_branch_override"`
	MaxConcurrentRuns      int       `json:"max_concurrent_runs"`
	AllowManual            bool      `json:"allow_manual"`
	AllowIssueLabel        bool      `json:"allow_issue_label"`
	AllowIssueEdit         bool      `json:"allow_issue_edit"`
	AllowPRComment         bool      `json:"allow_pr_comment"`
	AllowPRReview          bool      `json:"allow_pr_review"`
	AllowPRReviewComment   bool      `json:"allow_pr_review_comment"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type CreateRepositoryInput struct {
	FullName               string
	WebhookKey             string
	Enabled                bool
	EncryptedGitHubToken   []byte
	EncryptedWebhookSecret []byte
	AgentBackend           string
	AgentSessionMode       string
	BaseBranchOverride     string
	MaxConcurrentRuns      int
	AllowManual            bool
	AllowIssueLabel        bool
	AllowIssueEdit         bool
	AllowPRComment         bool
	AllowPRReview          bool
	AllowPRReviewComment   bool
}

type UpdateRepositoryInput struct {
	FullName               string
	WebhookKey             string
	Enabled                bool
	EncryptedGitHubToken   []byte
	EncryptedWebhookSecret []byte
	AgentBackend           string
	AgentSessionMode       string
	BaseBranchOverride     string
	MaxConcurrentRuns      int
	AllowManual            bool
	AllowIssueLabel        bool
	AllowIssueEdit         bool
	AllowPRComment         bool
	AllowPRReview          bool
	AllowPRReviewComment   bool
}

type RepositoryUserRole struct {
	RepoFullName string         `json:"repo_full_name"`
	UserID       string         `json:"user_id"`
	Role         RepositoryRole `json:"role"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
}

type UpsertRepositoryUserRoleInput struct {
	RepoFullName string
	UserID       string
	Role         RepositoryRole
}

type RepositoryStore interface {
	CreateRepository(in CreateRepositoryInput) (Repository, error)
	UpdateRepository(in UpdateRepositoryInput) (Repository, error)
	DeleteRepository(fullName string) error
	GetRepository(fullName string) (Repository, bool, error)
	GetRepositoryByWebhookKey(webhookKey string) (Repository, bool, error)
	ListRepositories() ([]Repository, error)
	CountRepositories() (int, error)
	UpsertRepositoryUserRole(in UpsertRepositoryUserRoleInput) (RepositoryUserRole, error)
	DeleteRepositoryUserRole(repoFullName, userID string) error
	GetRepositoryUserRole(repoFullName, userID string) (RepositoryUserRole, bool, error)
	ListRepositoryUserRoles(repoFullName string) ([]RepositoryUserRole, error)
	CountRunningRunsByRepo() (map[string]int, error)
}

func normalizeRepositoryRole(role RepositoryRole) (RepositoryRole, error) {
	switch strings.ToLower(strings.TrimSpace(string(role))) {
	case string(RepositoryRoleAdmin):
		return RepositoryRoleAdmin, nil
	case string(RepositoryRoleTrigger):
		return RepositoryRoleTrigger, nil
	default:
		return "", fmt.Errorf("invalid repository role %q", role)
	}
}

func (s *Store) CreateRepository(in CreateRepositoryInput) (Repository, error) {
	in.FullName = strings.TrimSpace(in.FullName)
	in.WebhookKey = strings.TrimSpace(in.WebhookKey)
	in.AgentBackend = strings.TrimSpace(in.AgentBackend)
	in.AgentSessionMode = strings.TrimSpace(in.AgentSessionMode)
	in.BaseBranchOverride = strings.TrimSpace(in.BaseBranchOverride)
	if in.FullName == "" {
		return Repository{}, fmt.Errorf("full_name is required")
	}
	if in.WebhookKey == "" {
		return Repository{}, fmt.Errorf("webhook_key is required")
	}
	if len(in.EncryptedGitHubToken) == 0 {
		return Repository{}, fmt.Errorf("encrypted github token is required")
	}
	if len(in.EncryptedWebhookSecret) == 0 {
		return Repository{}, fmt.Errorf("encrypted webhook secret is required")
	}
	if in.MaxConcurrentRuns < 0 {
		return Repository{}, fmt.Errorf("max_concurrent_runs cannot be negative")
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.InsertRepository(context.Background(), sqlitegen.InsertRepositoryParams{
		FullName:               in.FullName,
		WebhookKey:             in.WebhookKey,
		Enabled:                in.Enabled,
		EncryptedGithubToken:   in.EncryptedGitHubToken,
		EncryptedWebhookSecret: in.EncryptedWebhookSecret,
		AgentBackend:           in.AgentBackend,
		AgentSessionMode:       in.AgentSessionMode,
		BaseBranchOverride:     in.BaseBranchOverride,
		MaxConcurrentRuns:      int64(in.MaxConcurrentRuns),
		AllowManual:            in.AllowManual,
		AllowIssueLabel:        in.AllowIssueLabel,
		AllowIssueEdit:         in.AllowIssueEdit,
		AllowPrComment:         in.AllowPRComment,
		AllowPrReview:          in.AllowPRReview,
		AllowPrReviewComment:   in.AllowPRReviewComment,
		CreatedAt:              now,
		UpdatedAt:              now,
	}); err != nil {
		return Repository{}, fmt.Errorf("create repository %q: %w", in.FullName, err)
	}
	repo, ok, err := s.GetRepository(in.FullName)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, fmt.Errorf("repository %q not found after create", in.FullName)
	}
	return repo, nil
}

func (s *Store) UpdateRepository(in UpdateRepositoryInput) (Repository, error) {
	in.FullName = strings.TrimSpace(in.FullName)
	in.WebhookKey = strings.TrimSpace(in.WebhookKey)
	in.AgentBackend = strings.TrimSpace(in.AgentBackend)
	in.AgentSessionMode = strings.TrimSpace(in.AgentSessionMode)
	in.BaseBranchOverride = strings.TrimSpace(in.BaseBranchOverride)
	if in.FullName == "" {
		return Repository{}, fmt.Errorf("full_name is required")
	}
	if in.WebhookKey == "" {
		return Repository{}, fmt.Errorf("webhook_key is required")
	}
	if len(in.EncryptedGitHubToken) == 0 {
		return Repository{}, fmt.Errorf("encrypted github token is required")
	}
	if len(in.EncryptedWebhookSecret) == 0 {
		return Repository{}, fmt.Errorf("encrypted webhook secret is required")
	}
	if in.MaxConcurrentRuns < 0 {
		return Repository{}, fmt.Errorf("max_concurrent_runs cannot be negative")
	}
	rows, err := s.q.UpdateRepository(context.Background(), sqlitegen.UpdateRepositoryParams{
		WebhookKey:             in.WebhookKey,
		Enabled:                in.Enabled,
		EncryptedGithubToken:   in.EncryptedGitHubToken,
		EncryptedWebhookSecret: in.EncryptedWebhookSecret,
		AgentBackend:           in.AgentBackend,
		AgentSessionMode:       in.AgentSessionMode,
		BaseBranchOverride:     in.BaseBranchOverride,
		MaxConcurrentRuns:      int64(in.MaxConcurrentRuns),
		AllowManual:            in.AllowManual,
		AllowIssueLabel:        in.AllowIssueLabel,
		AllowIssueEdit:         in.AllowIssueEdit,
		AllowPrComment:         in.AllowPRComment,
		AllowPrReview:          in.AllowPRReview,
		AllowPrReviewComment:   in.AllowPRReviewComment,
		UpdatedAt:              time.Now().UTC().UnixNano(),
		FullName:               in.FullName,
	})
	if err != nil {
		return Repository{}, fmt.Errorf("update repository %q: %w", in.FullName, err)
	}
	if rows == 0 {
		return Repository{}, fmt.Errorf("repository %q not found", in.FullName)
	}
	repo, ok, err := s.GetRepository(in.FullName)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, fmt.Errorf("repository %q not found after update", in.FullName)
	}
	return repo, nil
}

func (s *Store) DeleteRepository(fullName string) error {
	fullName = strings.TrimSpace(fullName)
	if fullName == "" {
		return nil
	}
	_, err := s.q.DeleteRepository(context.Background(), fullName)
	if err != nil {
		return fmt.Errorf("delete repository %q: %w", fullName, err)
	}
	return nil
}

func (s *Store) GetRepository(fullName string) (Repository, bool, error) {
	row, err := s.q.GetRepositoryByFullName(context.Background(), strings.TrimSpace(fullName))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repository{}, false, nil
		}
		return Repository{}, false, fmt.Errorf("get repository %q: %w", fullName, err)
	}
	return fromDBRepository(row), true, nil
}

func (s *Store) GetRepositoryByWebhookKey(webhookKey string) (Repository, bool, error) {
	row, err := s.q.GetRepositoryByWebhookKey(context.Background(), strings.TrimSpace(webhookKey))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repository{}, false, nil
		}
		return Repository{}, false, fmt.Errorf("get repository by webhook key: %w", err)
	}
	return fromDBRepository(row), true, nil
}

func (s *Store) ListRepositories() ([]Repository, error) {
	rows, err := s.q.ListRepositories(context.Background())
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	out := make([]Repository, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBRepository(row))
	}
	return out, nil
}

func (s *Store) CountRepositories() (int, error) {
	count, err := s.q.CountRepositories(context.Background())
	if err != nil {
		return 0, fmt.Errorf("count repositories: %w", err)
	}
	return int(count), nil
}

func (s *Store) UpsertRepositoryUserRole(in UpsertRepositoryUserRoleInput) (RepositoryUserRole, error) {
	in.RepoFullName = strings.TrimSpace(in.RepoFullName)
	in.UserID = strings.TrimSpace(in.UserID)
	role, err := normalizeRepositoryRole(in.Role)
	if err != nil {
		return RepositoryUserRole{}, err
	}
	if in.RepoFullName == "" || in.UserID == "" {
		return RepositoryUserRole{}, fmt.Errorf("repo_full_name and user_id are required")
	}
	now := time.Now().UTC().UnixNano()
	if err := s.q.UpsertRepositoryUserRole(context.Background(), sqlitegen.UpsertRepositoryUserRoleParams{
		RepoFullName: in.RepoFullName,
		UserID:       in.UserID,
		Role:         string(role),
		CreatedAt:    now,
		UpdatedAt:    now,
	}); err != nil {
		return RepositoryUserRole{}, fmt.Errorf("upsert repository role %q/%q: %w", in.RepoFullName, in.UserID, err)
	}
	row, err := s.q.GetRepositoryUserRole(context.Background(), sqlitegen.GetRepositoryUserRoleParams{
		RepoFullName: in.RepoFullName,
		UserID:       in.UserID,
	})
	if err != nil {
		return RepositoryUserRole{}, fmt.Errorf("get repository role after upsert: %w", err)
	}
	return fromDBRepositoryUserRole(row), nil
}

func (s *Store) DeleteRepositoryUserRole(repoFullName, userID string) error {
	repoFullName = strings.TrimSpace(repoFullName)
	userID = strings.TrimSpace(userID)
	if repoFullName == "" || userID == "" {
		return nil
	}
	_, err := s.q.DeleteRepositoryUserRole(context.Background(), sqlitegen.DeleteRepositoryUserRoleParams{
		RepoFullName: repoFullName,
		UserID:       userID,
	})
	if err != nil {
		return fmt.Errorf("delete repository role %q/%q: %w", repoFullName, userID, err)
	}
	return nil
}

func (s *Store) GetRepositoryUserRole(repoFullName, userID string) (RepositoryUserRole, bool, error) {
	row, err := s.q.GetRepositoryUserRole(context.Background(), sqlitegen.GetRepositoryUserRoleParams{
		RepoFullName: strings.TrimSpace(repoFullName),
		UserID:       strings.TrimSpace(userID),
	})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return RepositoryUserRole{}, false, nil
		}
		return RepositoryUserRole{}, false, fmt.Errorf("get repository role %q/%q: %w", repoFullName, userID, err)
	}
	return fromDBRepositoryUserRole(row), true, nil
}

func (s *Store) ListRepositoryUserRoles(repoFullName string) ([]RepositoryUserRole, error) {
	rows, err := s.q.ListRepositoryUserRoles(context.Background(), strings.TrimSpace(repoFullName))
	if err != nil {
		return nil, fmt.Errorf("list repository roles for %q: %w", repoFullName, err)
	}
	out := make([]RepositoryUserRole, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromDBRepositoryUserRole(row))
	}
	return out, nil
}

func (s *Store) CountRunningRunsByRepo() (map[string]int, error) {
	rows, err := s.q.CountRunningRunsByRepo(context.Background())
	if err != nil {
		return nil, fmt.Errorf("count running runs by repo: %w", err)
	}
	out := make(map[string]int, len(rows))
	for _, row := range rows {
		repo := strings.TrimSpace(row.Repo)
		if repo == "" {
			continue
		}
		out[repo] = int(row.RunCount)
	}
	return out, nil
}

func fromDBRepository(row any) Repository {
	switch r := row.(type) {
	case sqlitegen.Repository:
		return fromDBRepositoryParts(r.FullName, r.WebhookKey, r.Enabled, r.EncryptedGithubToken, r.EncryptedWebhookSecret, r.AgentBackend, r.AgentSessionMode, r.BaseBranchOverride, r.MaxConcurrentRuns, r.AllowManual, r.AllowIssueLabel, r.AllowIssueEdit, r.AllowPrComment, r.AllowPrReview, r.AllowPrReviewComment, r.CreatedAt, r.UpdatedAt)
	default:
		return Repository{}
	}
}

func fromDBRepositoryParts(fullName, webhookKey string, enabled bool, encryptedGitHubToken, encryptedWebhookSecret []byte, agentBackend, agentSessionMode, baseBranchOverride string, maxConcurrentRuns int64, allowManual, allowIssueLabel, allowIssueEdit, allowPRComment, allowPRReview, allowPRReviewComment bool, createdAt, updatedAt int64) Repository {
	return Repository{
		FullName:               fullName,
		WebhookKey:             webhookKey,
		Enabled:                enabled,
		EncryptedGitHubToken:   encryptedGitHubToken,
		EncryptedWebhookSecret: encryptedWebhookSecret,
		AgentBackend:           agentBackend,
		AgentSessionMode:       agentSessionMode,
		BaseBranchOverride:     baseBranchOverride,
		MaxConcurrentRuns:      int(maxConcurrentRuns),
		AllowManual:            allowManual,
		AllowIssueLabel:        allowIssueLabel,
		AllowIssueEdit:         allowIssueEdit,
		AllowPRComment:         allowPRComment,
		AllowPRReview:          allowPRReview,
		AllowPRReviewComment:   allowPRReviewComment,
		CreatedAt:              time.Unix(0, createdAt).UTC(),
		UpdatedAt:              time.Unix(0, updatedAt).UTC(),
	}
}

func fromDBRepositoryUserRole(row any) RepositoryUserRole {
	switch r := row.(type) {
	case sqlitegen.RepositoryUserRole:
		return fromDBRepositoryUserRoleParts(r.RepoFullName, r.UserID, r.Role, r.CreatedAt, r.UpdatedAt)
	default:
		return RepositoryUserRole{}
	}
}

func fromDBRepositoryUserRoleParts(repoFullName, userID, role string, createdAt, updatedAt int64) RepositoryUserRole {
	return RepositoryUserRole{
		RepoFullName: repoFullName,
		UserID:       userID,
		Role:         RepositoryRole(strings.ToLower(strings.TrimSpace(role))),
		CreatedAt:    time.Unix(0, createdAt).UTC(),
		UpdatedAt:    time.Unix(0, updatedAt).UTC(),
	}
}
