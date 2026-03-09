package repositories

import (
	"errors"
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/agent"
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
	AgentBackend           agent.Backend
	AgentSessionMode       agent.SessionMode
	BaseBranchDefault      string
	MaxConcurrentRuns      int
	AllowManual            bool
	AllowedWebhookTriggers AllowedWebhookTriggers
}

type Resolver interface {
	Resolve(fullName string) (ResolvedRepoConfig, error)
	ResolveByWebhookKey(key string) (ResolvedRepoConfig, error)
}

type ConfigResolver struct {
	store              state.RepositoryStore
	cipher             credentials.Cipher
	defaultAgent       agent.Backend
	defaultSessionMode agent.SessionMode
}

func NewResolver(store state.RepositoryStore, cipher credentials.Cipher, defaultAgent agent.Backend, defaultSessionMode agent.SessionMode) *ConfigResolver {
	return &ConfigResolver{
		store:              store,
		cipher:             cipher,
		defaultAgent:       agent.NormalizeBackend(string(defaultAgent)),
		defaultSessionMode: agent.NormalizeSessionMode(string(defaultSessionMode)),
	}
}

func (r *ConfigResolver) Resolve(fullName string) (ResolvedRepoConfig, error) {
	repo, ok, err := r.store.GetRepository(strings.TrimSpace(fullName))
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("get repository %q: %w", strings.TrimSpace(fullName), err)
	}
	if !ok {
		return ResolvedRepoConfig{}, ErrNotFound
	}
	return r.resolveRecord(repo)
}

func (r *ConfigResolver) ResolveByWebhookKey(key string) (ResolvedRepoConfig, error) {
	repo, ok, err := r.store.GetRepositoryByWebhookKey(strings.TrimSpace(key))
	if err != nil {
		return ResolvedRepoConfig{}, fmt.Errorf("get repository by webhook key %q: %w", strings.TrimSpace(key), err)
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

	agentBackend := agent.NormalizeBackend(repo.AgentBackend)
	if strings.TrimSpace(repo.AgentBackend) == "" {
		agentBackend = r.defaultAgent
	}
	agentSessionMode := agent.NormalizeSessionMode(repo.AgentSessionMode)
	if strings.TrimSpace(repo.AgentSessionMode) == "" {
		agentSessionMode = r.defaultSessionMode
	}

	return ResolvedRepoConfig{
		FullName:          strings.TrimSpace(repo.FullName),
		Enabled:           repo.Enabled,
		GitHubToken:       strings.TrimSpace(string(githubTokenRaw)),
		WebhookSecret:     strings.TrimSpace(string(webhookSecretRaw)),
		WebhookKey:        strings.TrimSpace(repo.WebhookKey),
		AgentBackend:      agentBackend,
		AgentSessionMode:  agentSessionMode,
		BaseBranchDefault: strings.TrimSpace(repo.BaseBranchOverride),
		MaxConcurrentRuns: repo.MaxConcurrentRuns,
		AllowManual:       repo.AllowManual,
		AllowedWebhookTriggers: AllowedWebhookTriggers{
			IssueLabel:      repo.AllowIssueLabel,
			IssueEdit:       repo.AllowIssueEdit,
			PRComment:       repo.AllowPRComment,
			PRReview:        repo.AllowPRReview,
			PRReviewComment: repo.AllowPRReviewComment,
		},
	}, nil
}
