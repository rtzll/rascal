package state

import (
	"database/sql"
	"testing"
)

func TestParseRunAndTaskStatus(t *testing.T) {
	t.Parallel()

	if got, ok := ParseRunStatus(" review "); !ok || got != StatusReview {
		t.Fatalf("ParseRunStatus(review) = %q, %t", got, ok)
	}
	if got, ok := ParseRunStatus(""); !ok || got != StatusQueued {
		t.Fatalf("ParseRunStatus(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseRunStatus("paused"); ok {
		t.Fatal("expected invalid run status to be rejected")
	}

	if got, ok := ParseTaskStatus(" completed "); !ok || got != TaskCompleted {
		t.Fatalf("ParseTaskStatus(completed) = %q, %t", got, ok)
	}
	if got, ok := ParseTaskStatus(""); !ok || got != TaskOpen {
		t.Fatalf("ParseTaskStatus(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseTaskStatus("archived"); ok {
		t.Fatal("expected invalid task status to be rejected")
	}
}

func TestFromDBNormalizesTaskAndRunStatus(t *testing.T) {
	t.Parallel()

	task := fromDBTaskParts("task-1", "owner/repo", "codex", 0, 0, "completed ", 0, "", 0, 0)
	if task.Status != TaskCompleted {
		t.Fatalf("task status = %q, want %q", task.Status, TaskCompleted)
	}

	fallbackTask := fromDBTaskParts("task-2", "owner/repo", "codex", 0, 0, "archived", 0, "", 0, 0)
	if fallbackTask.Status != TaskOpen {
		t.Fatalf("task fallback status = %q, want %q", fallbackTask.Status, TaskOpen)
	}

	run := fromDBRunParts(
		"run-1", "task-1", "owner/repo", "test", "codex", "main", "", "cli", true,
		" running ", "/tmp/run-1", 0, 0, "", "", "", "", "", 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if run.Status != StatusRunning {
		t.Fatalf("run status = %q, want %q", run.Status, StatusRunning)
	}

	fallbackRun := fromDBRunParts(
		"run-2", "task-1", "owner/repo", "test", "codex", "main", "", "cli", true,
		"stuck", "/tmp/run-2", 0, 0, "", "", "", "", "", 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if fallbackRun.Status != StatusQueued {
		t.Fatalf("run fallback status = %q, want %q", fallbackRun.Status, StatusQueued)
	}
}
