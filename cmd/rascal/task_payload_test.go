package main

import (
	"testing"

	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestBuildCreateTaskPayloadForRun(t *testing.T) {
	t.Parallel()

	req := buildCreateTaskPayload(createTaskPayloadInput{
		Repo:        "owner/repo",
		Instruction: "Fix flaky tests",
		BaseBranch:  "main",
	})

	if req.path != "/v1/tasks" {
		t.Fatalf("path = %q, want /v1/tasks", req.path)
	}
	if req.task == nil {
		t.Fatal("expected task payload")
	}
	if req.issueTask != nil {
		t.Fatal("did not expect issue payload")
	}
	if req.task.Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", req.task.Repo)
	}
	if req.task.Instruction != "Fix flaky tests" {
		t.Fatalf("task = %q, want Fix flaky tests", req.task.Instruction)
	}
	if req.task.BaseBranch != "main" {
		t.Fatalf("base_branch = %q, want main", req.task.BaseBranch)
	}
	if req.task.TaskID != "" {
		t.Fatalf("did not expect task_id in run payload")
	}
	if req.task.Trigger != "" {
		t.Fatalf("did not expect trigger in run payload")
	}
	if req.task.Debug != nil {
		t.Fatalf("did not expect debug in run payload when unset")
	}
}

func TestBuildCreateTaskPayloadForRetry(t *testing.T) {
	t.Parallel()

	debug := false
	req := buildCreateTaskPayload(createTaskPayloadInput{
		TaskID:      "task_1",
		Repo:        "owner/repo",
		Instruction: "Retry task",
		BaseBranch:  "main",
		Trigger:     runtrigger.NameRetry,
		Debug:       &debug,
	})

	if req.path != "/v1/tasks" {
		t.Fatalf("path = %q, want /v1/tasks", req.path)
	}
	if req.task == nil {
		t.Fatal("expected task payload")
	}
	if req.task.TaskID != "task_1" {
		t.Fatalf("task_id = %q, want task_1", req.task.TaskID)
	}
	if req.task.Trigger != "retry" {
		t.Fatalf("trigger = %q, want retry", req.task.Trigger)
	}
	if req.task.Debug == nil || *req.task.Debug != false {
		t.Fatalf("debug = %v, want false", req.task.Debug)
	}
}

func TestBuildCreateTaskPayloadForIssue(t *testing.T) {
	t.Parallel()

	debug := true
	req := buildCreateTaskPayload(createTaskPayloadInput{
		Repo:        "owner/repo",
		IssueNumber: 42,
		Instruction: "ignored",
		BaseBranch:  "ignored",
		Trigger:     runtrigger.Name("ignored"),
		Debug:       &debug,
	})

	if req.path != "/v1/tasks/issue" {
		t.Fatalf("path = %q, want /v1/tasks/issue", req.path)
	}
	if req.issueTask == nil {
		t.Fatal("expected issue payload")
	}
	if req.task != nil {
		t.Fatal("did not expect task payload")
	}
	if req.issueTask.Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", req.issueTask.Repo)
	}
	if req.issueTask.IssueNumber != 42 {
		t.Fatalf("issue_number = %d, want 42", req.issueTask.IssueNumber)
	}
	if req.issueTask.Debug == nil || *req.issueTask.Debug != true {
		t.Fatalf("debug = %v, want true", req.issueTask.Debug)
	}
}
