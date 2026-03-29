package state

import (
	"database/sql"
	"testing"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
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

	task := fromDBTaskParts("task-1", "owner/repo", "codex", 0, 0, false, "completed ", 0, "", 0, 0)
	if task.Status != TaskCompleted {
		t.Fatalf("task status = %q, want %q", task.Status, TaskCompleted)
	}

	fallbackTask := fromDBTaskParts("task-2", "owner/repo", "codex", 0, 0, false, "archived", 0, "", 0, 0)
	if fallbackTask.Status != TaskOpen {
		t.Fatalf("task fallback status = %q, want %q", fallbackTask.Status, TaskOpen)
	}

	run := fromDBRunParts(
		"run-1", "task-1", "owner/repo", "test", "codex", "main", "", "cli", true,
		" running ", "/tmp/run-1", 0, 0, "", "", "", "", "", "", "pending", "", "", sql.NullInt64{}, sql.NullInt64{}, 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if run.Status != StatusRunning {
		t.Fatalf("run status = %q, want %q", run.Status, StatusRunning)
	}

	fallbackRun := fromDBRunParts(
		"run-2", "task-1", "owner/repo", "test", "codex", "main", "", "cli", true,
		"stuck", "/tmp/run-2", 0, 0, "", "", "", "", "", "", "pending", "", "", sql.NullInt64{}, sql.NullInt64{}, 0, 0, sql.NullInt64{}, sql.NullInt64{},
	)
	if fallbackRun.Status != StatusQueued {
		t.Fatalf("run fallback status = %q, want %q", fallbackRun.Status, StatusQueued)
	}
}

func TestCreateRunInputWithDefaults(t *testing.T) {
	t.Parallel()

	in, err := (CreateRunInput{
		ID:           " run-1 ",
		TaskID:       " task-1 ",
		Repo:         "Owner/Repo",
		Instruction:  "task",
		AgentRuntime: runtime.Runtime(""),
		PRNumber:     42,
	}).WithDefaults()
	if err != nil {
		t.Fatalf("WithDefaults: %v", err)
	}

	if in.ID != "run-1" {
		t.Fatalf("ID = %q, want run-1", in.ID)
	}
	if in.TaskID != "task-1" {
		t.Fatalf("TaskID = %q, want task-1", in.TaskID)
	}
	if in.Repo != "owner/repo" {
		t.Fatalf("Repo = %q, want owner/repo", in.Repo)
	}
	if in.AgentRuntime != runtime.RuntimeGooseCodex {
		t.Fatalf("AgentRuntime = %q, want %q", in.AgentRuntime, runtime.RuntimeGooseCodex)
	}
	if in.BaseBranch != "main" {
		t.Fatalf("BaseBranch = %q, want main", in.BaseBranch)
	}
	if in.Trigger != runtrigger.NameCLI {
		t.Fatalf("Trigger = %q, want %q", in.Trigger, runtrigger.NameCLI)
	}
	if in.Debug == nil || !*in.Debug {
		t.Fatalf("Debug = %v, want true", in.Debug)
	}
	if in.PRStatus != PRStatusOpen {
		t.Fatalf("PRStatus = %q, want %q", in.PRStatus, PRStatusOpen)
	}
}

func TestCreateRunInputWithDefaultsRejectsUnknownTrigger(t *testing.T) {
	t.Parallel()

	_, err := (CreateRunInput{
		ID:      "run-1",
		TaskID:  "task-1",
		Repo:    "owner/repo",
		Trigger: runtrigger.Name("unknown"),
	}).WithDefaults()
	if err == nil {
		t.Fatal("expected invalid trigger error")
	}
}
