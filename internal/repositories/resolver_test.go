package repositories

import (
	"path/filepath"
	"testing"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

func TestResolverResolveAndWebhookLookup(t *testing.T) {
	t.Parallel()

	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cipher, err := credentials.NewAESCipher("resolver-test-key")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	encToken, err := cipher.Encrypt([]byte("gh-token"))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	encSecret, err := cipher.Encrypt([]byte("wh-secret"))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	if _, err := store.CreateRepository(state.CreateRepositoryInput{
		FullName:               "owner/repo",
		WebhookKey:             "00112233445566778899aabbccddeeff",
		Enabled:                true,
		EncryptedGitHubToken:   encToken,
		EncryptedWebhookSecret: encSecret,
		AgentBackend:           "goose",
		AgentSessionMode:       "all",
		BaseBranchOverride:     "main",
		MaxConcurrentRuns:      3,
		AllowManual:            true,
		AllowIssueLabel:        true,
		AllowIssueEdit:         true,
		AllowPRComment:         true,
		AllowPRReview:          true,
		AllowPRReviewComment:   true,
	}); err != nil {
		t.Fatalf("create repository: %v", err)
	}

	resolver := NewResolver(store, cipher, agent.BackendCodex, agent.SessionModePROnly)
	resolved, err := resolver.Resolve("owner/repo")
	if err != nil {
		t.Fatalf("resolve by full name: %v", err)
	}
	if resolved.GitHubToken != "gh-token" {
		t.Fatalf("github token = %q, want gh-token", resolved.GitHubToken)
	}
	if resolved.WebhookSecret != "wh-secret" {
		t.Fatalf("webhook secret = %q, want wh-secret", resolved.WebhookSecret)
	}
	if resolved.AgentBackend != agent.BackendGoose {
		t.Fatalf("agent backend = %s, want goose", resolved.AgentBackend)
	}
	if resolved.AgentSessionMode != agent.SessionModeAll {
		t.Fatalf("agent session mode = %s, want all", resolved.AgentSessionMode)
	}
	if resolved.BaseBranchDefault != "main" {
		t.Fatalf("base branch default = %q, want main", resolved.BaseBranchDefault)
	}

	byKey, err := resolver.ResolveByWebhookKey("00112233445566778899aabbccddeeff")
	if err != nil {
		t.Fatalf("resolve by webhook key: %v", err)
	}
	if byKey.FullName != "owner/repo" {
		t.Fatalf("resolved full_name = %q, want owner/repo", byKey.FullName)
	}
}

func TestResolverFallsBackToDaemonDefaults(t *testing.T) {
	t.Parallel()

	store, err := state.New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	cipher, err := credentials.NewAESCipher("resolver-default-key")
	if err != nil {
		t.Fatalf("new cipher: %v", err)
	}
	encToken, err := cipher.Encrypt([]byte("gh-token"))
	if err != nil {
		t.Fatalf("encrypt token: %v", err)
	}
	encSecret, err := cipher.Encrypt([]byte("wh-secret"))
	if err != nil {
		t.Fatalf("encrypt secret: %v", err)
	}
	if _, err := store.CreateRepository(state.CreateRepositoryInput{
		FullName:               "owner/repo",
		WebhookKey:             "ffeeddccbbaa99887766554433221100",
		Enabled:                true,
		EncryptedGitHubToken:   encToken,
		EncryptedWebhookSecret: encSecret,
		AgentBackend:           "",
		AgentSessionMode:       "",
		BaseBranchOverride:     "",
		MaxConcurrentRuns:      0,
		AllowManual:            true,
		AllowIssueLabel:        true,
		AllowIssueEdit:         true,
		AllowPRComment:         true,
		AllowPRReview:          true,
		AllowPRReviewComment:   true,
	}); err != nil {
		t.Fatalf("create repository: %v", err)
	}

	resolver := NewResolver(store, cipher, agent.BackendCodex, agent.SessionModePROnly)
	resolved, err := resolver.Resolve("owner/repo")
	if err != nil {
		t.Fatalf("resolve by full name: %v", err)
	}
	if resolved.AgentBackend != agent.BackendCodex {
		t.Fatalf("agent backend = %s, want codex", resolved.AgentBackend)
	}
	if resolved.AgentSessionMode != agent.SessionModePROnly {
		t.Fatalf("agent session mode = %s, want pr-only", resolved.AgentSessionMode)
	}
}
