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
	EncryptedWebhookSecret []byte    `json:"-"`
	CreatedAt              time.Time `json:"created_at"`
	UpdatedAt              time.Time `json:"updated_at"`
}

type CreateRepositoryInput struct {
	FullName               string
	WebhookKey             string
	Enabled                bool
	EncryptedWebhookSecret []byte
}

type UpdateRepositoryInput struct {
	FullName               string
	Enabled                *bool
	EncryptedWebhookSecret []byte
}

func (s *Store) CreateRepository(in CreateRepositoryInput) (Repository, error) {
	fullName := NormalizeRepo(in.FullName)
	webhookKey := strings.TrimSpace(in.WebhookKey)
	if fullName == "" || webhookKey == "" || len(in.EncryptedWebhookSecret) == 0 {
		return Repository{}, fmt.Errorf("full_name, webhook_key and encrypted webhook secret are required")
	}

	now := time.Now().UTC().UnixNano()
	_, err := s.db.ExecContext(context.Background(), `
		INSERT INTO repositories (
			full_name,
			webhook_key,
			enabled,
			encrypted_webhook_secret,
			created_at,
			updated_at
		) VALUES (?, ?, ?, ?, ?, ?)
	`, fullName, webhookKey, in.Enabled, in.EncryptedWebhookSecret, now, now)
	if err != nil {
		return Repository{}, fmt.Errorf("create repository %q: %w", fullName, err)
	}
	repo, ok, err := s.GetRepository(fullName)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, fmt.Errorf("load repository %q after create: not found", fullName)
	}
	return repo, nil
}

func (s *Store) GetRepository(fullName string) (Repository, bool, error) {
	return s.getRepositoryByQuery(`
		SELECT full_name, webhook_key, enabled, encrypted_webhook_secret, created_at, updated_at
		FROM repositories
		WHERE full_name = ?
	`, NormalizeRepo(fullName))
}

func (s *Store) GetRepositoryByWebhookKey(webhookKey string) (Repository, bool, error) {
	return s.getRepositoryByQuery(`
		SELECT full_name, webhook_key, enabled, encrypted_webhook_secret, created_at, updated_at
		FROM repositories
		WHERE webhook_key = ?
	`, strings.TrimSpace(webhookKey))
}

func (s *Store) ListRepositories() ([]Repository, error) {
	rows, err := s.db.QueryContext(context.Background(), `
		SELECT full_name, webhook_key, enabled, encrypted_webhook_secret, created_at, updated_at
		FROM repositories
		ORDER BY full_name
	`)
	if err != nil {
		return nil, fmt.Errorf("list repositories: %w", err)
	}
	defer func() {
		if closeErr := rows.Close(); closeErr != nil {
			err = fmt.Errorf("close repositories rows: %w", closeErr)
		}
	}()

	var repos []Repository
	for rows.Next() {
		repo, err := scanRepository(rows)
		if err != nil {
			return nil, err
		}
		repos = append(repos, repo)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate repositories: %w", err)
	}
	return repos, nil
}

func (s *Store) UpdateRepository(in UpdateRepositoryInput) (Repository, error) {
	fullName := NormalizeRepo(in.FullName)
	if fullName == "" {
		return Repository{}, fmt.Errorf("full_name is required")
	}
	repo, ok, err := s.GetRepository(fullName)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, fmt.Errorf("repository %q not found", fullName)
	}

	enabled := repo.Enabled
	if in.Enabled != nil {
		enabled = *in.Enabled
	}
	secret := repo.EncryptedWebhookSecret
	if len(in.EncryptedWebhookSecret) > 0 {
		secret = in.EncryptedWebhookSecret
	}

	_, err = s.db.ExecContext(context.Background(), `
		UPDATE repositories
		SET enabled = ?, encrypted_webhook_secret = ?, updated_at = ?
		WHERE full_name = ?
	`, enabled, secret, time.Now().UTC().UnixNano(), fullName)
	if err != nil {
		return Repository{}, fmt.Errorf("update repository %q: %w", fullName, err)
	}

	updated, ok, err := s.GetRepository(fullName)
	if err != nil {
		return Repository{}, err
	}
	if !ok {
		return Repository{}, fmt.Errorf("load repository %q after update: not found", fullName)
	}
	return updated, nil
}

func (s *Store) getRepositoryByQuery(query, arg string) (Repository, bool, error) {
	row := s.db.QueryRowContext(context.Background(), query, arg)
	repo, err := scanRepository(row)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Repository{}, false, nil
		}
		return Repository{}, false, err
	}
	return repo, true, nil
}

type repositoryScanner interface {
	Scan(dest ...any) error
}

func scanRepository(scanner repositoryScanner) (Repository, error) {
	var repo Repository
	var createdAt int64
	var updatedAt int64
	if err := scanner.Scan(
		&repo.FullName,
		&repo.WebhookKey,
		&repo.Enabled,
		&repo.EncryptedWebhookSecret,
		&createdAt,
		&updatedAt,
	); err != nil {
		return Repository{}, fmt.Errorf("scan repository: %w", err)
	}
	repo.CreatedAt = time.Unix(0, createdAt).UTC()
	repo.UpdatedAt = time.Unix(0, updatedAt).UTC()
	return repo, nil
}
