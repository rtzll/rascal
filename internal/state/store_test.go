package state

import (
	"path/filepath"
	"testing"
	"time"
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
		Trigger:     "issue_label",
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
		r.Status = StatusAwaitingFeedback
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
