package state

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestStoreRunAndTaskLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, err := store.UpsertTask(UpsertTaskInput{ID: "repo#1", Repo: "owner/repo", IssueNumber: 1}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	run, err := store.AddRun(CreateRunInput{
		ID:          "run_1",
		TaskID:      "repo#1",
		Repo:        "owner/repo",
		Task:        "Implement feature",
		BaseBranch:  "main",
		HeadBranch:  "rascal/repo-1/run_1",
		Trigger:     runtrigger.NameIssueLabel,
		RunDir:      "/tmp/run_1",
		IssueNumber: 1,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if run.Status != StatusQueued {
		t.Fatalf("expected queued run status, got %s", run.Status)
	}
	if !run.Debug {
		t.Fatal("expected debug=true by default")
	}

	debugOff := false
	run2, err := store.AddRun(CreateRunInput{
		ID:         "run_2",
		TaskID:     "repo#1",
		Repo:       "owner/repo",
		Task:       "No debug",
		BaseBranch: "main",
		HeadBranch: "rascal/repo-1/run_2",
		RunDir:     "/tmp/run_2",
		Debug:      &debugOff,
	})
	if err != nil {
		t.Fatalf("add second run: %v", err)
	}
	if run2.Debug {
		t.Fatal("expected debug=false when explicitly requested")
	}

	if _, err := store.SetRunStatus(run.ID, StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	if _, err := store.UpdateRun(run.ID, func(r *Run) error {
		r.Status = StatusReview
		r.PRNumber = 123
		return nil
	}); err != nil {
		t.Fatalf("update run: %v", err)
	}

	if err := store.SetTaskPR("repo#1", "owner/repo", 123); err != nil {
		t.Fatalf("set task pr: %v", err)
	}

	task, ok := store.FindTaskByPR("owner/repo", 123)
	if !ok {
		t.Fatal("expected task lookup by pr to succeed")
	}
	if task.ID != "repo#1" {
		t.Fatalf("unexpected task id: %s", task.ID)
	}

	if err := store.MarkTaskCompleted("repo#1"); err != nil {
		t.Fatalf("mark task completed: %v", err)
	}
	if !store.IsTaskCompleted("repo#1") {
		t.Fatal("expected task to be completed")
	}
	if err := store.MarkTaskOpen("repo#1"); err != nil {
		t.Fatalf("mark task open: %v", err)
	}
	if store.IsTaskCompleted("repo#1") {
		t.Fatal("expected task to be reopened")
	}
}

func TestStoreAddRunRejectsUnknownTrigger(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	_, err = store.AddRun(CreateRunInput{
		ID:      "run_invalid_trigger",
		TaskID:  "repo#1",
		Repo:    "owner/repo",
		Task:    "Reject invalid trigger",
		Trigger: runtrigger.Name("unknown_trigger"),
		RunDir:  "/tmp/run_invalid_trigger",
	})
	if err == nil || !strings.Contains(err.Error(), "invalid trigger") {
		t.Fatalf("expected invalid trigger error, got %v", err)
	}
}

func TestStoreFindTaskByPRNormalizesRepoCase(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, err := store.UpsertTask(UpsertTaskInput{ID: "repo#77", Repo: "owner/repo", PRNumber: 77}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	task, ok := store.FindTaskByPR("Owner/Repo", 77)
	if !ok {
		t.Fatal("expected mixed-case repo lookup by pr to succeed")
	}
	if task.Repo != "owner/repo" {
		t.Fatalf("task repo = %q, want owner/repo", task.Repo)
	}
}

func TestStoreAllowsTaskSessionBackendMigration(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	task, err := store.UpsertTask(UpsertTaskInput{
		ID:           "repo#2",
		Repo:         "owner/repo",
		AgentBackend: agent.BackendGoose,
		IssueNumber:  2,
	})
	if err != nil {
		t.Fatalf("upsert goose task: %v", err)
	}
	if task.AgentBackend != agent.BackendGoose {
		t.Fatalf("task backend = %s, want %s", task.AgentBackend, agent.BackendGoose)
	}

	session, err := store.UpsertTaskAgentSession(UpsertTaskAgentSessionInput{
		TaskID:           task.ID,
		AgentBackend:     agent.BackendGoose,
		BackendSessionID: "goose-session",
		SessionKey:       "owner-repo-2",
		SessionRoot:      "/tmp/goose-session",
		LastRunID:        "run_goose",
	})
	if err != nil {
		t.Fatalf("upsert goose task session: %v", err)
	}
	if session.AgentBackend != agent.BackendGoose {
		t.Fatalf("session backend = %s, want %s", session.AgentBackend, agent.BackendGoose)
	}

	task, err = store.UpsertTask(UpsertTaskInput{
		ID:           task.ID,
		Repo:         task.Repo,
		AgentBackend: agent.BackendCodex,
		IssueNumber:  task.IssueNumber,
	})
	if err != nil {
		t.Fatalf("migrate task backend to codex: %v", err)
	}
	if task.AgentBackend != agent.BackendCodex {
		t.Fatalf("task backend = %s, want %s", task.AgentBackend, agent.BackendCodex)
	}

	session, err = store.UpsertTaskAgentSession(UpsertTaskAgentSessionInput{
		TaskID:           task.ID,
		AgentBackend:     agent.BackendCodex,
		BackendSessionID: "",
		SessionKey:       "owner-repo-2",
		SessionRoot:      "/tmp/codex-session",
		LastRunID:        "run_codex",
	})
	if err != nil {
		t.Fatalf("migrate task session backend to codex: %v", err)
	}
	if session.AgentBackend != agent.BackendCodex {
		t.Fatalf("session backend = %s, want %s", session.AgentBackend, agent.BackendCodex)
	}
	if session.BackendSessionID != "" {
		t.Fatalf("session id = %q, want empty after backend migration", session.BackendSessionID)
	}
	if session.SessionRoot != "/tmp/codex-session" {
		t.Fatalf("session root = %q, want /tmp/codex-session", session.SessionRoot)
	}
}

func TestStoreAllowsRecoveryTransitionRunningToQueued(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-recovery", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(CreateRunInput{
		ID:         "run_recovery",
		TaskID:     "task-recovery",
		Repo:       "owner/repo",
		Task:       "recovery",
		BaseBranch: "main",
		RunDir:     "/tmp/run_recovery",
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	if _, err := store.SetRunStatus(run.ID, StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	updated, err := store.UpdateRun(run.ID, func(r *Run) error {
		r.Status = StatusQueued
		r.Error = ""
		r.StartedAt = nil
		r.CompletedAt = nil
		return nil
	})
	if err != nil {
		t.Fatalf("requeue running run: %v", err)
	}
	if updated.Status != StatusQueued {
		t.Fatalf("status = %s, want queued", updated.Status)
	}
}

func TestStoreRejectsInvalidRunStatusTransition(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-transition", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(CreateRunInput{
		ID:         "run_transition",
		TaskID:     "task-transition",
		Repo:       "owner/repo",
		Task:       "transition",
		BaseBranch: "main",
		RunDir:     "/tmp/run_transition",
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if _, err := store.SetRunStatus(run.ID, StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := store.SetRunStatus(run.ID, StatusSucceeded, ""); err != nil {
		t.Fatalf("set succeeded: %v", err)
	}
	if _, err := store.SetRunStatus(run.ID, StatusRunning, "retrying"); err == nil {
		t.Fatal("expected invalid transition error")
	} else if !strings.Contains(err.Error(), "invalid run status transition") {
		t.Fatalf("unexpected transition error: %v", err)
	}

	got, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if got.Status != StatusSucceeded {
		t.Fatalf("status = %s, want succeeded", got.Status)
	}

	if _, err := store.SetRunStatus(run.ID, RunStatus("paused"), ""); err == nil {
		t.Fatal("expected invalid run status to be rejected")
	} else if !strings.Contains(err.Error(), "invalid run status") {
		t.Fatalf("unexpected invalid status error: %v", err)
	}
}

func TestParseCredentialScopeAndStatus(t *testing.T) {
	t.Parallel()

	if got, ok := ParseCredentialScope(" shared "); !ok || got != CredentialScopeShared {
		t.Fatalf("ParseCredentialScope(shared) = %q, %t", got, ok)
	}
	if got, ok := ParseCredentialScope(""); !ok || got != CredentialScopePersonal {
		t.Fatalf("ParseCredentialScope(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseCredentialScope("team"); ok {
		t.Fatal("expected invalid credential scope to be rejected")
	}

	if got, ok := ParseCredentialStatus(" cooldown "); !ok || got != CredentialStatusCooldown {
		t.Fatalf("ParseCredentialStatus(cooldown) = %q, %t", got, ok)
	}
	if got, ok := ParseCredentialStatus(""); !ok || got != CredentialStatusActive {
		t.Fatalf("ParseCredentialStatus(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseCredentialStatus("paused"); ok {
		t.Fatal("expected invalid credential status to be rejected")
	}
}

func TestStoreRejectsInvalidCredentialScopeAndStatus(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if _, err := store.CreateCodexCredential(CreateCodexCredentialInput{
		ID:                "cred-invalid-scope",
		Scope:             CredentialScope("team"),
		EncryptedAuthBlob: []byte("blob"),
	}); err == nil {
		t.Fatal("expected invalid credential scope to fail")
	}

	if _, err := store.CreateCodexCredential(CreateCodexCredentialInput{
		ID:                "cred-invalid-status",
		Scope:             CredentialScopeShared,
		Status:            CredentialStatus("paused"),
		EncryptedAuthBlob: []byte("blob"),
	}); err == nil {
		t.Fatal("expected invalid credential status to fail")
	}

	if err := store.SetCodexCredentialStatus("cred-unknown", CredentialStatus("paused"), nil, "bad"); err == nil {
		t.Fatal("expected invalid status update to fail")
	}
}

func TestStoreUpsertRunTokenUsage(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-usage", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(CreateRunInput{
		ID:         "run_usage",
		TaskID:     "task-usage",
		Repo:       "owner/repo",
		Task:       "usage",
		BaseBranch: "main",
		RunDir:     "/tmp/run_usage",
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	inputTokens := int64(120)
	outputTokens := int64(30)
	cachedInputTokens := int64(40)
	reasoningOutputTokens := int64(10)
	usage, err := store.UpsertRunTokenUsage(RunTokenUsage{
		RunID:                 run.ID,
		Backend:               agent.BackendGoose,
		Provider:              "openai",
		Model:                 "gpt-5-codex",
		TotalTokens:           150,
		InputTokens:           &inputTokens,
		OutputTokens:          &outputTokens,
		CachedInputTokens:     &cachedInputTokens,
		ReasoningOutputTokens: &reasoningOutputTokens,
		RawUsageJSON:          `{"total_tokens":150}`,
		CapturedAt:            time.Now().UTC(),
	})
	if err != nil {
		t.Fatalf("upsert run token usage: %v", err)
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", usage.TotalTokens)
	}

	got, ok := store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected run token usage for %s", run.ID)
	}
	if got.Model != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", got.Model)
	}
	if got.InputTokens == nil || *got.InputTokens != 120 {
		t.Fatalf("input_tokens = %v, want 120", got.InputTokens)
	}
	if got.CachedInputTokens == nil || *got.CachedInputTokens != 40 {
		t.Fatalf("cached_input_tokens = %v, want 40", got.CachedInputTokens)
	}
}

func TestStoreUpdateRunDoesNotInferPRStatusFromRunStatus(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-pr-status", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := store.AddRun(CreateRunInput{
		ID:         "run_pr_status",
		TaskID:     "task-pr-status",
		Repo:       "owner/repo",
		Task:       "pr status behavior",
		BaseBranch: "main",
		RunDir:     "/tmp/run_pr_status",
		PRNumber:   42,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if run.PRStatus != PRStatusOpen {
		t.Fatalf("initial pr status = %s, want open", run.PRStatus)
	}

	if _, err := store.SetRunStatus(run.ID, StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}
	if _, err := store.UpdateRun(run.ID, func(r *Run) error {
		r.Status = StatusReview
		r.PRStatus = PRStatusNone
		return nil
	}); err != nil {
		t.Fatalf("set review with explicit none pr status: %v", err)
	}

	if _, err := store.SetRunStatus(run.ID, StatusCanceled, "canceled"); err != nil {
		t.Fatalf("set canceled: %v", err)
	}
	got, ok := store.GetRun(run.ID)
	if !ok {
		t.Fatalf("run %s not found", run.ID)
	}
	if got.PRStatus != PRStatusNone {
		t.Fatalf("pr status = %s, want none", got.PRStatus)
	}
}

func TestStoreSeenDelivery(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if store.DeliverySeen("delivery-1") {
		t.Fatal("expected first delivery to be unseen")
	}
	claim, claimed, err := store.ClaimDelivery("delivery-1", "test")
	if err != nil {
		t.Fatalf("claim delivery first call: %v", err)
	}
	if !claimed {
		t.Fatal("expected first delivery claim to succeed")
	}
	if err := store.CompleteDeliveryClaim(claim); err != nil {
		t.Fatalf("complete delivery first claim: %v", err)
	}
	if !store.DeliverySeen("delivery-1") {
		t.Fatal("expected delivery to be seen after record")
	}
	_, claimed, err = store.ClaimDelivery("delivery-1", "test-2")
	if err != nil {
		t.Fatalf("claim delivery second call: %v", err)
	}
	if claimed {
		t.Fatal("expected second delivery claim to be rejected as duplicate")
	}
}

func TestStoreReleaseDeliveryClaimAllowsRetry(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	claim, claimed, err := store.ClaimDelivery("delivery-2", "test")
	if err != nil {
		t.Fatalf("claim delivery: %v", err)
	}
	if !claimed {
		t.Fatal("expected claim")
	}
	if err := store.ReleaseDeliveryClaim(claim); err != nil {
		t.Fatalf("release claim: %v", err)
	}
	retryClaim, claimed, err := store.ClaimDelivery("delivery-2", "test-retry")
	if err != nil {
		t.Fatalf("retry claim: %v", err)
	}
	if !claimed {
		t.Fatal("expected retry claim after release")
	}
	if err := store.CompleteDeliveryClaim(retryClaim); err != nil {
		t.Fatalf("complete retry claim: %v", err)
	}
}

func TestStoreClaimRunStartAtomic(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-claim", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_claim_1",
		TaskID:     "task-claim",
		Repo:       "owner/repo",
		Task:       "first",
		BaseBranch: "main",
		RunDir:     "/tmp/run_claim_1",
	}); err != nil {
		t.Fatalf("add run 1: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_claim_2",
		TaskID:     "task-claim",
		Repo:       "owner/repo",
		Task:       "second",
		BaseBranch: "main",
		RunDir:     "/tmp/run_claim_2",
	}); err != nil {
		t.Fatalf("add run 2: %v", err)
	}

	if _, claimed, err := store.ClaimRunStart("run_claim_1"); err != nil {
		t.Fatalf("claim run 1: %v", err)
	} else if !claimed {
		t.Fatal("expected run 1 claim to succeed")
	}
	if run, claimed, err := store.ClaimRunStart("run_claim_2"); err != nil {
		t.Fatalf("claim run 2: %v", err)
	} else if claimed {
		t.Fatalf("expected run 2 claim to fail while task has running run, got status=%s", run.Status)
	}
}

func TestStoreClaimNextQueuedRun(t *testing.T) {
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

	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_a_1",
		TaskID:     "task-a",
		Repo:       "owner/repo",
		Task:       "a1",
		BaseBranch: "main",
		RunDir:     "/tmp/run_a_1",
	}); err != nil {
		t.Fatalf("add run_a_1: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_b_1",
		TaskID:     "task-b",
		Repo:       "owner/repo",
		Task:       "b1",
		BaseBranch: "main",
		RunDir:     "/tmp/run_b_1",
	}); err != nil {
		t.Fatalf("add run_b_1: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_a_2",
		TaskID:     "task-a",
		Repo:       "owner/repo",
		Task:       "a2",
		BaseBranch: "main",
		RunDir:     "/tmp/run_a_2",
	}); err != nil {
		t.Fatalf("add run_a_2: %v", err)
	}

	claimed, ok, err := store.ClaimNextQueuedRun("task-a")
	if err != nil {
		t.Fatalf("claim preferred task-a: %v", err)
	}
	if !ok {
		t.Fatal("expected claim for task-a")
	}
	if claimed.ID != "run_a_1" {
		t.Fatalf("expected run_a_1, got %s", claimed.ID)
	}

	if run, ok, err := store.ClaimNextQueuedRun("task-a"); err != nil {
		t.Fatalf("claim while task-a active: %v", err)
	} else if !ok {
		t.Fatal("expected claim for another task while task-a is active")
	} else if run.ID != "run_b_1" {
		t.Fatalf("expected run_b_1, got %s", run.ID)
	}

	if _, ok, err := store.ClaimNextQueuedRun(""); err != nil {
		t.Fatalf("claim when only blocked queued run remains: %v", err)
	} else if ok {
		t.Fatal("expected no claim while only queued run is blocked by active task")
	}
}

func TestStoreRunLeaseLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if err := store.UpsertRunLease("run_lease_1", "instance-a", 2*time.Minute); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}
	if got := store.CountRunLeasesByOwner("instance-a"); got != 1 {
		t.Fatalf("expected owner lease count 1, got %d", got)
	}
	lease, ok := store.GetRunLease("run_lease_1")
	if !ok {
		t.Fatal("expected run lease to exist")
	}
	if lease.OwnerID != "instance-a" {
		t.Fatalf("unexpected owner id: %s", lease.OwnerID)
	}

	if ok, err := store.RenewRunLease("run_lease_1", "instance-a", 2*time.Minute); err != nil {
		t.Fatalf("renew run lease: %v", err)
	} else if !ok {
		t.Fatal("expected renew by owner to succeed")
	}

	if ok, err := store.RenewRunLease("run_lease_1", "instance-b", 2*time.Minute); err != nil {
		t.Fatalf("renew run lease with wrong owner: %v", err)
	} else if ok {
		t.Fatal("expected renew by non-owner to fail")
	}

	if err := store.DeleteRunLease("run_lease_1"); err != nil {
		t.Fatalf("delete run lease: %v", err)
	}
	if got := store.CountRunLeasesByOwner("instance-a"); got != 0 {
		t.Fatalf("expected owner lease count 0 after delete, got %d", got)
	}
	if _, ok := store.GetRunLease("run_lease_1"); ok {
		t.Fatal("expected run lease to be deleted")
	}
}

func TestStoreDeleteRunLeaseForOwner(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if err := store.UpsertRunLease("run_lease_owner", "instance-a", 2*time.Minute); err != nil {
		t.Fatalf("upsert run lease: %v", err)
	}

	if err := store.DeleteRunLeaseForOwner("run_lease_owner", "instance-b"); err != nil {
		t.Fatalf("delete run lease for wrong owner: %v", err)
	}
	if lease, ok := store.GetRunLease("run_lease_owner"); !ok || lease.OwnerID != "instance-a" {
		t.Fatalf("expected lease to remain with instance-a, got %+v ok=%t", lease, ok)
	}

	if err := store.DeleteRunLeaseForOwner("run_lease_owner", "instance-a"); err != nil {
		t.Fatalf("delete run lease for owner: %v", err)
	}
	if _, ok := store.GetRunLease("run_lease_owner"); ok {
		t.Fatal("expected run lease to be deleted for the matching owner")
	}
}

func TestStoreRunExecutionLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	exec, err := store.UpsertRunExecution(RunExecution{
		RunID:         "run_exec_1",
		Backend:       RunExecutionBackendDocker,
		ContainerName: "rascal-run_exec_1",
		ContainerID:   "container-abc",
		Status:        RunExecutionStatusRunning,
		ExitCode:      0,
	})
	if err != nil {
		t.Fatalf("upsert run execution: %v", err)
	}
	if exec.Status != RunExecutionStatusRunning {
		t.Fatalf("unexpected initial execution status: %s", exec.Status)
	}

	loaded, ok := store.GetRunExecution("run_exec_1")
	if !ok {
		t.Fatal("expected persisted run execution")
	}
	if loaded.ContainerID != "container-abc" {
		t.Fatalf("unexpected container id: %s", loaded.ContainerID)
	}

	updated, err := store.UpdateRunExecutionState("run_exec_1", RunExecutionStatusExited, 137, time.Now().UTC())
	if err != nil {
		t.Fatalf("update run execution state: %v", err)
	}
	if updated.Status != RunExecutionStatusExited {
		t.Fatalf("expected exited status, got %s", updated.Status)
	}
	if updated.ExitCode != 137 {
		t.Fatalf("expected exit code 137, got %d", updated.ExitCode)
	}

	if err := store.DeleteRunExecution("run_exec_1"); err != nil {
		t.Fatalf("delete run execution: %v", err)
	}
	if _, ok := store.GetRunExecution("run_exec_1"); ok {
		t.Fatal("expected run execution to be deleted")
	}
}

func TestStoreListRunningRuns(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-running", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_running",
		TaskID:     "task-running",
		Repo:       "owner/repo",
		Task:       "running",
		BaseBranch: "main",
		RunDir:     "/tmp/run_running",
	}); err != nil {
		t.Fatalf("add run_running: %v", err)
	}
	if _, err := store.AddRun(CreateRunInput{
		ID:         "run_queued",
		TaskID:     "task-running",
		Repo:       "owner/repo",
		Task:       "queued",
		BaseBranch: "main",
		RunDir:     "/tmp/run_queued",
	}); err != nil {
		t.Fatalf("add run_queued: %v", err)
	}
	if _, err := store.SetRunStatus("run_running", StatusRunning, ""); err != nil {
		t.Fatalf("set running: %v", err)
	}

	running := store.ListRunningRuns()
	if len(running) != 1 {
		t.Fatalf("expected exactly 1 running run, got %d", len(running))
	}
	if running[0].ID != "run_running" {
		t.Fatalf("expected run_running, got %s", running[0].ID)
	}
}

func TestStoreRunCancelLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	if err := store.RequestRunCancel("run_cancel_1", "canceled by user", "user"); err != nil {
		t.Fatalf("request run cancel: %v", err)
	}
	cancelReq, ok := store.GetRunCancel("run_cancel_1")
	if !ok {
		t.Fatal("expected cancel request to exist")
	}
	if cancelReq.Reason != "canceled by user" {
		t.Fatalf("unexpected cancel reason: %q", cancelReq.Reason)
	}
	if cancelReq.Source != "user" {
		t.Fatalf("unexpected cancel source: %q", cancelReq.Source)
	}

	if err := store.RequestRunCancel("run_cancel_1", "orchestrator shutdown", "shutdown"); err != nil {
		t.Fatalf("update run cancel: %v", err)
	}
	cancelReq, ok = store.GetRunCancel("run_cancel_1")
	if !ok {
		t.Fatal("expected cancel request after update")
	}
	if cancelReq.Source != "shutdown" {
		t.Fatalf("expected updated source shutdown, got %q", cancelReq.Source)
	}

	if err := store.ClearRunCancel("run_cancel_1"); err != nil {
		t.Fatalf("clear run cancel: %v", err)
	}
	if _, ok := store.GetRunCancel("run_cancel_1"); ok {
		t.Fatal("expected cancel request to be cleared")
	}
}

func TestNormalizeRunPipelineConfigDefaults(t *testing.T) {
	t.Parallel()

	cfg, err := NormalizeRunPipelineConfig(RunPipelineConfig{Enabled: true})
	if err != nil {
		t.Fatalf("normalize config: %v", err)
	}
	if len(cfg.Phases) != 3 {
		t.Fatalf("expected 3 default phases, got %d", len(cfg.Phases))
	}
	if cfg.MaxPhases != 3 {
		t.Fatalf("max phases = %d, want 3", cfg.MaxPhases)
	}
	if cfg.MaxChildRunsPerPhase != 1 {
		t.Fatalf("max child runs per phase = %d, want 1", cfg.MaxChildRunsPerPhase)
	}
}

func TestStoreRunPipelineLimitExhaustion(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}
	if _, err := store.UpsertTask(UpsertTaskInput{ID: "task-pipeline", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}

	pipeline, err := store.CreateRunPipeline(CreateRunPipelineInput{
		ID:          "pipe_limit",
		TaskID:      "task-pipeline",
		Repo:        "owner/repo",
		Task:        "pipeline task",
		BaseBranch:  "main",
		HeadBranch:  "rascal/task-pipeline",
		Trigger:     "cli",
		ArtifactDir: filepath.Join(t.TempDir(), "artifacts"),
		Config: RunPipelineConfig{
			Enabled:   true,
			MaxPhases: 2,
			Phases: []PipelinePhaseName{
				PipelinePhasePlan,
				PipelinePhaseImplement,
				PipelinePhaseVerify,
			},
		},
	})
	if err != nil {
		t.Fatalf("create pipeline: %v", err)
	}

	startPhase := func(runID string, phase PipelinePhaseName) {
		if _, _, err := store.StartRunPipelinePhaseChild(StartRunPipelinePhaseChildInput{
			PipelineID: pipeline.ID,
			PhaseName:  phase,
			Run: CreateRunInput{
				ID:           runID,
				TaskID:       pipeline.TaskID,
				Repo:         pipeline.Repo,
				Task:         pipeline.Task,
				AgentBackend: agent.BackendCodex,
				BaseBranch:   pipeline.BaseBranch,
				HeadBranch:   pipeline.HeadBranch,
				Trigger:      runtrigger.Normalize(pipeline.Trigger),
				RunDir:       filepath.Join(t.TempDir(), runID),
			},
		}); err != nil {
			t.Fatalf("start phase %s: %v", phase, err)
		}
	}

	startPhase("run_plan", PipelinePhasePlan)
	if _, _, err := store.FinalizeRunPipelinePhase(FinalizeRunPipelinePhaseInput{
		PipelineID:    pipeline.ID,
		PhaseName:     PipelinePhasePlan,
		State:         PipelinePhaseStateSucceeded,
		ArtifactPaths: []string{"/tmp/plan.json"},
	}); err != nil {
		t.Fatalf("finalize plan: %v", err)
	}

	startPhase("run_implement", PipelinePhaseImplement)
	updated, _, err := store.FinalizeRunPipelinePhase(FinalizeRunPipelinePhaseInput{
		PipelineID:    pipeline.ID,
		PhaseName:     PipelinePhaseImplement,
		State:         PipelinePhaseStateSucceeded,
		ArtifactPaths: []string{"/tmp/implementation-summary.json"},
	})
	if err != nil {
		t.Fatalf("finalize implement: %v", err)
	}
	if updated.Status != PipelineStatusFailed {
		t.Fatalf("pipeline status = %s, want failed", updated.Status)
	}
	if updated.FailedPhase != PipelinePhaseVerify {
		t.Fatalf("pipeline failed phase = %s, want verify", updated.FailedPhase)
	}

	detail, ok := store.GetRunPipelineDetail(updated.ID)
	if !ok {
		t.Fatal("expected pipeline detail")
	}
	if got := detail.Phases[2].State; got != PipelinePhaseStateSkipped {
		t.Fatalf("verify phase state = %s, want skipped", got)
	}
}
