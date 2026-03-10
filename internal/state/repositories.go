package state

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Repository struct {
	FullName               string    `json:"full_name"`
	WebhookKey             string    `json:"webhook_key"`
	Enabled                bool      `json:"enabled"`
	EncryptedGitHubToken   []byte    `json:"-"`
	EncryptedWebhookSecret []byte    `json:"-"`
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
	AllowManual            bool
	AllowIssueLabel        bool
	AllowIssueEdit         bool
	AllowPRComment         bool
	AllowPRReview          bool
	AllowPRReviewComment   bool
}

type RepositoryStore interface {
	CreateRepository(in CreateRepositoryInput) (Repository, error)
	GetRepository(fullName string) (Repository, bool, error)
	GetRepositoryByWebhookKey(webhookKey string) (Repository, bool, error)
	CountRepositories() (int, error)
}

func (s *Store) CreateRepository(in CreateRepositoryInput) (Repository, error) {
	in.FullName = NormalizeRepo(in.FullName)
	in.WebhookKey = strings.TrimSpace(in.WebhookKey)
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

	now := time.Now().UTC().UnixNano()
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO repositories (
			full_name,
			webhook_key,
			enabled,
			encrypted_github_token,
			encrypted_webhook_secret,
			allow_manual,
			allow_issue_label,
			allow_issue_edit,
			allow_pr_comment,
			allow_pr_review,
			allow_pr_review_comment,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`,
		in.FullName,
		in.WebhookKey,
		in.Enabled,
		in.EncryptedGitHubToken,
		in.EncryptedWebhookSecret,
		in.AllowManual,
		in.AllowIssueLabel,
		in.AllowIssueEdit,
		in.AllowPRComment,
		in.AllowPRReview,
		in.AllowPRReviewComment,
		now,
		now,
	)
	if err != nil {
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

func (s *Store) GetRepository(fullName string) (Repository, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT
			full_name,
			webhook_key,
			enabled,
			encrypted_github_token,
			encrypted_webhook_secret,
			allow_manual,
			allow_issue_label,
			allow_issue_edit,
			allow_pr_comment,
			allow_pr_review,
			allow_pr_review_comment,
			created_at,
			updated_at
		FROM repositories
		WHERE full_name = ?
	`, NormalizeRepo(fullName))
	repo, err := scanRepository(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repository{}, false, nil
		}
		return Repository{}, false, fmt.Errorf("get repository %q: %w", NormalizeRepo(fullName), err)
	}
	return repo, true, nil
}

func (s *Store) GetRepositoryByWebhookKey(webhookKey string) (Repository, bool, error) {
	row := s.db.QueryRowContext(context.Background(), `
		SELECT
			full_name,
			webhook_key,
			enabled,
			encrypted_github_token,
			encrypted_webhook_secret,
			allow_manual,
			allow_issue_label,
			allow_issue_edit,
			allow_pr_comment,
			allow_pr_review,
			allow_pr_review_comment,
			created_at,
			updated_at
		FROM repositories
		WHERE webhook_key = ?
	`, strings.TrimSpace(webhookKey))
	repo, err := scanRepository(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repository{}, false, nil
		}
		return Repository{}, false, fmt.Errorf("get repository by webhook key %q: %w", strings.TrimSpace(webhookKey), err)
	}
	return repo, true, nil
}

func (s *Store) CountRepositories() (int, error) {
	var count int
	if err := s.db.QueryRowContext(context.Background(), `SELECT COUNT(*) FROM repositories`).Scan(&count); err != nil {
		return 0, fmt.Errorf("count repositories: %w", err)
	}
	return count, nil
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanRepository(row rowScanner) (Repository, error) {
	var (
		repo          Repository
		createdAtUnix int64
		updatedAtUnix int64
	)
	err := row.Scan(
		&repo.FullName,
		&repo.WebhookKey,
		&repo.Enabled,
		&repo.EncryptedGitHubToken,
		&repo.EncryptedWebhookSecret,
		&repo.AllowManual,
		&repo.AllowIssueLabel,
		&repo.AllowIssueEdit,
		&repo.AllowPRComment,
		&repo.AllowPRReview,
		&repo.AllowPRReviewComment,
		&createdAtUnix,
		&updatedAtUnix,
	)
	if err != nil {
		return Repository{}, fmt.Errorf("scan repository row: %w", err)
	}
	repo.CreatedAt = time.Unix(0, createdAtUnix).UTC()
	repo.UpdatedAt = time.Unix(0, updatedAtUnix).UTC()
	return repo, nil
}
