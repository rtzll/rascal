package state

import (
	"path/filepath"
	"testing"
	"time"
)

func TestStoreRepositoryLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	repo, err := store.CreateRepository(CreateRepositoryInput{
		FullName:               "owner/repo",
		WebhookKey:             "00112233445566778899aabbccddeeff",
		Enabled:                true,
		EncryptedGitHubToken:   []byte("enc-token"),
		EncryptedWebhookSecret: []byte("enc-secret"),
		AgentBackend:           "codex",
		AgentSessionMode:       "pr-only",
		BaseBranchOverride:     "main",
		MaxConcurrentRuns:      2,
		AllowManual:            true,
		AllowIssueLabel:        true,
		AllowIssueEdit:         true,
		AllowPRComment:         true,
		AllowPRReview:          true,
		AllowPRReviewComment:   true,
	})
	if err != nil {
		t.Fatalf("create repository: %v", err)
	}
	if repo.FullName != "owner/repo" {
		t.Fatalf("full_name = %q, want owner/repo", repo.FullName)
	}

	byName, ok, err := store.GetRepository("owner/repo")
	if err != nil {
		t.Fatalf("get repository by name: %v", err)
	}
	if !ok {
		t.Fatal("expected repository by name")
	}
	if byName.WebhookKey != repo.WebhookKey {
		t.Fatalf("webhook_key = %q, want %q", byName.WebhookKey, repo.WebhookKey)
	}

	byKey, ok, err := store.GetRepositoryByWebhookKey(repo.WebhookKey)
	if err != nil {
		t.Fatalf("get repository by webhook key: %v", err)
	}
	if !ok {
		t.Fatal("expected repository by webhook key")
	}
	if byKey.FullName != "owner/repo" {
		t.Fatalf("full_name by key = %q, want owner/repo", byKey.FullName)
	}

	updated, err := store.UpdateRepository(UpdateRepositoryInput{
		FullName:               repo.FullName,
		WebhookKey:             repo.WebhookKey,
		Enabled:                false,
		EncryptedGitHubToken:   []byte("enc-token-2"),
		EncryptedWebhookSecret: []byte("enc-secret-2"),
		AgentBackend:           "",
		AgentSessionMode:       "",
		BaseBranchOverride:     "",
		MaxConcurrentRuns:      0,
		AllowManual:            false,
		AllowIssueLabel:        false,
		AllowIssueEdit:         true,
		AllowPRComment:         true,
		AllowPRReview:          true,
		AllowPRReviewComment:   true,
	})
	if err != nil {
		t.Fatalf("update repository: %v", err)
	}
	if updated.Enabled {
		t.Fatal("expected enabled=false after update")
	}
	if updated.AllowManual {
		t.Fatal("expected allow_manual=false after update")
	}

	repos, err := store.ListRepositories()
	if err != nil {
		t.Fatalf("list repositories: %v", err)
	}
	if len(repos) != 1 {
		t.Fatalf("repository count = %d, want 1", len(repos))
	}
	count, err := store.CountRepositories()
	if err != nil {
		t.Fatalf("count repositories: %v", err)
	}
	if count != 1 {
		t.Fatalf("count repositories = %d, want 1", count)
	}

	if err := store.DeleteRepository("owner/repo"); err != nil {
		t.Fatalf("delete repository: %v", err)
	}
	if _, ok, err := store.GetRepository("owner/repo"); err != nil {
		t.Fatalf("get repository after delete: %v", err)
	} else if ok {
		t.Fatal("expected repository to be deleted")
	}
}

func TestStoreRepositoryUserRoles(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertUser(UpsertUserInput{
		ID:            "user-1",
		ExternalLogin: "alice",
		Role:          UserRoleUser,
	}); err != nil {
		t.Fatalf("upsert user: %v", err)
	}
	if _, err := store.CreateRepository(CreateRepositoryInput{
		FullName:               "owner/repo",
		WebhookKey:             "feedfacefeedfacefeedfacefeedface",
		Enabled:                true,
		EncryptedGitHubToken:   []byte("enc-token"),
		EncryptedWebhookSecret: []byte("enc-secret"),
		AllowManual:            true,
		AllowIssueLabel:        true,
		AllowIssueEdit:         true,
		AllowPRComment:         true,
		AllowPRReview:          true,
		AllowPRReviewComment:   true,
	}); err != nil {
		t.Fatalf("create repository: %v", err)
	}

	role, err := store.UpsertRepositoryUserRole(UpsertRepositoryUserRoleInput{
		RepoFullName: "owner/repo",
		UserID:       "user-1",
		Role:         RepositoryRoleTrigger,
	})
	if err != nil {
		t.Fatalf("upsert repository role: %v", err)
	}
	if role.Role != RepositoryRoleTrigger {
		t.Fatalf("role = %s, want trigger", role.Role)
	}

	got, ok, err := store.GetRepositoryUserRole("owner/repo", "user-1")
	if err != nil {
		t.Fatalf("get repository role: %v", err)
	}
	if !ok {
		t.Fatal("expected repository role")
	}
	if got.Role != RepositoryRoleTrigger {
		t.Fatalf("role = %s, want trigger", got.Role)
	}

	roles, err := store.ListRepositoryUserRoles("owner/repo")
	if err != nil {
		t.Fatalf("list repository roles: %v", err)
	}
	if len(roles) != 1 {
		t.Fatalf("roles count = %d, want 1", len(roles))
	}

	if err := store.DeleteRepositoryUserRole("owner/repo", "user-1"); err != nil {
		t.Fatalf("delete repository role: %v", err)
	}
	if _, ok, err := store.GetRepositoryUserRole("owner/repo", "user-1"); err != nil {
		t.Fatalf("get repository role after delete: %v", err)
	} else if ok {
		t.Fatal("expected repository role to be deleted")
	}
}

func TestStoreQueuedRunSchedulingHelpers(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	for _, taskID := range []string{"task-a", "task-b"} {
		if _, err := store.UpsertTask(UpsertTaskInput{ID: taskID, Repo: "owner/repo"}); err != nil {
			t.Fatalf("upsert task %s: %v", taskID, err)
		}
	}
	a, err := store.AddRun(CreateRunInput{
		ID:         "run_a",
		TaskID:     "task-a",
		Repo:       "owner/repo",
		Task:       "A",
		BaseBranch: "main",
		RunDir:     "/tmp/run_a",
	})
	if err != nil {
		t.Fatalf("add run a: %v", err)
	}
	b, err := store.AddRun(CreateRunInput{
		ID:         "run_b",
		TaskID:     "task-b",
		Repo:       "owner/other",
		Task:       "B",
		BaseBranch: "main",
		RunDir:     "/tmp/run_b",
	})
	if err != nil {
		t.Fatalf("add run b: %v", err)
	}

	queuedAll, err := store.ListQueuedRunsOrdered(10)
	if err != nil {
		t.Fatalf("list queued runs ordered: %v", err)
	}
	if len(queuedAll) != 2 {
		t.Fatalf("queued runs count = %d, want 2", len(queuedAll))
	}
	if queuedAll[0].ID != a.ID {
		t.Fatalf("first queued run = %s, want %s", queuedAll[0].ID, a.ID)
	}

	queuedTask, err := store.ListQueuedRunsForTask("task-b", 10)
	if err != nil {
		t.Fatalf("list queued runs for task: %v", err)
	}
	if len(queuedTask) != 1 || queuedTask[0].ID != b.ID {
		t.Fatalf("queued task runs = %+v, want only %s", queuedTask, b.ID)
	}

	claimed, ok, err := store.ClaimQueuedRunByID(a.ID)
	if err != nil {
		t.Fatalf("claim queued run by id: %v", err)
	}
	if !ok {
		t.Fatal("expected claim to succeed")
	}
	if claimed.Status != StatusRunning {
		t.Fatalf("claimed run status = %s, want running", claimed.Status)
	}

	runningByRepo, err := store.CountRunningRunsByRepo()
	if err != nil {
		t.Fatalf("count running runs by repo: %v", err)
	}
	if runningByRepo["owner/repo"] != 1 {
		t.Fatalf("running count for owner/repo = %d, want 1", runningByRepo["owner/repo"])
	}

	_, ok, err = store.ClaimQueuedRunByID(a.ID)
	if err != nil {
		t.Fatalf("second claim queued run by id: %v", err)
	}
	if ok {
		t.Fatal("expected second claim attempt to return ok=false")
	}

	// Leave no stale running row for downstream tests sharing filesystem clocks.
	if _, err := store.UpdateRun(a.ID, func(r *Run) error {
		now := time.Now().UTC()
		r.Status = StatusSucceeded
		r.CompletedAt = &now
		return nil
	}); err != nil {
		t.Fatalf("mark claimed run succeeded: %v", err)
	}
}
