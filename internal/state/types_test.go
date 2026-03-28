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

	if got, ok := ParseRunExecutionStatus(" stopping "); !ok || got != RunExecutionStatusStopping {
		t.Fatalf("ParseRunExecutionStatus(stopping) = %q, %t", got, ok)
	}
	if got, ok := ParseRunExecutionStatus(""); !ok || got != RunExecutionStatusCreated {
		t.Fatalf("ParseRunExecutionStatus(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseRunExecutionStatus("paused"); ok {
		t.Fatal("expected invalid run execution status to be rejected")
	}

	if got, ok := ParseRunExecutionBackend(" noop "); !ok || got != RunExecutionBackendNoop {
		t.Fatalf("ParseRunExecutionBackend(noop) = %q, %t", got, ok)
	}
	if got, ok := ParseRunExecutionBackend(""); !ok || got != RunExecutionBackendDocker {
		t.Fatalf("ParseRunExecutionBackend(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseRunExecutionBackend("podman"); ok {
		t.Fatal("expected invalid run execution backend to be rejected")
	}

	if got, ok := ParseDeliveryStatus(" processed "); !ok || got != DeliveryStatusProcessed {
		t.Fatalf("ParseDeliveryStatus(processed) = %q, %t", got, ok)
	}
	if got, ok := ParseDeliveryStatus(""); !ok || got != DeliveryStatusProcessing {
		t.Fatalf("ParseDeliveryStatus(empty) = %q, %t", got, ok)
	}
	if _, ok := ParseDeliveryStatus("queued"); ok {
		t.Fatal("expected invalid delivery status to be rejected")
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
		" running ", "/tmp/run-1", 0, 0, "", "", "", "", "", "", "", "", 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if run.Status != StatusRunning {
		t.Fatalf("run status = %q, want %q", run.Status, StatusRunning)
	}

	fallbackRun := fromDBRunParts(
		"run-2", "task-1", "owner/repo", "test", "codex", "main", "", "cli", true,
		"stuck", "/tmp/run-2", 0, 0, "", "", "", "", "", "", "", "", 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if fallbackRun.Status != StatusQueued {
		t.Fatalf("run fallback status = %q, want %q", fallbackRun.Status, StatusQueued)
	}
}
