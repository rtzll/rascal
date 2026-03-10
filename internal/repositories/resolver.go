package repositories

import (
	"errors"
	"fmt"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

var ErrNotFound = errors.New("repository not found")

type AllowedWebhookTriggers struct {
	IssueLabel      bool
	IssueEdit       bool
	PRComment       bool
	PRReview        bool
	PRReviewComment bool
}

type ResolvedRepoConfig struct {
	FullName               string
	Enabled                bool
	GitHubToken            string
	WebhookSecret          string
	WebhookKey             string
	AllowManual            bool
	AllowedWebhookTriggers AllowedWebhookTriggers
}

type Resolver interface {
	Resolve(fullName string) (ResolvedRepoConfig, error)
	ResolveByWebhookKey(key string) (ResolvedRepoConfig, error)
}

type ConfigResolver struct {
	store  state.RepositoryStore
	cipher credentials.Cipher
}

func NewResolver(store state.RepositoryStore, cipher credentials.Cipher) *ConfigResolver {
	return &ConfigResolver{store: store, cipher: cipher}
}

func (r *ConfigResolver) Resolve(fullName string) (ResolvedRepoConfig, error) {
	repo, ok, err := r.store.GetRepository(fullName)
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("get repository %q: %w", fullName, err)
	}
	if !ok {
		return ResolvedRepoConfig{}, ErrNotFound
	}
	return r.resolveRecord(repo)
}

func (r *ConfigResolver) ResolveByWebhookKey(key string) (ResolvedRepoConfig, error) {
	repo, ok, err := r.store.GetRepositoryByWebhookKey(key)
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("get repository by webhook key %q: %w", key, err)
	}
	if !ok {
		return ResolvedRepoConfig{}, ErrNotFound
	}
	return r.resolveRecord(repo)
}

func (r *ConfigResolver) resolveRecord(repo state.Repository) (ResolvedRepoConfig, error) {
	githubTokenRaw, err := r.cipher.Decrypt(repo.EncryptedGitHubToken)
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("decrypt repository github token for %q: %w", repo.FullName, err)
	}
	webhookSecretRaw, err := r.cipher.Decrypt(repo.EncryptedWebhookSecret)
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("decrypt repository webhook secret for %q: %w", repo.FullName, err)
	}

	return ResolvedRepoConfig{
		FullName:      repo.FullName,
		Enabled:       repo.Enabled,
		GitHubToken:   string(githubTokenRaw),
		WebhookSecret: string(webhookSecretRaw),
		WebhookKey:    repo.WebhookKey,
		AllowManual:   repo.AllowManual,
		AllowedWebhookTriggers: AllowedWebhookTriggers{
			IssueLabel:      repo.AllowIssueLabel,
			IssueEdit:       repo.AllowIssueEdit,
			PRComment:       repo.AllowPRComment,
			PRReview:        repo.AllowPRReview,
			PRReviewComment: repo.AllowPRReviewComment,
		},
	}, nil
}
