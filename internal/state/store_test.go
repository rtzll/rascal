package state

import (
	"path/filepath"
	"testing"
)

func TestStoreRunAndTaskLifecycle(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.json"), 200)
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

	store, err := New(filepath.Join(t.TempDir(), "state.json"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	seen, err := store.SeenDelivery("delivery-1")
	if err != nil {
		t.Fatalf("seen delivery first call: %v", err)
	}
	if seen {
		t.Fatal("expected first delivery to be unseen")
	}

	seen, err = store.SeenDelivery("delivery-1")
	if err != nil {
		t.Fatalf("seen delivery second call: %v", err)
	}
	if !seen {
		t.Fatal("expected second delivery to be seen")
	}
}
