package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/state"
)

func TestHandleRunLogsRespectsLines(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_logs",
		TaskID:      "task_logs",
		Repo:        "owner/repo",
		Instruction: "show logs",
		BaseBranch:  "main",
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	var runnerLog strings.Builder
	var gooseLog strings.Builder
	for i := 1; i <= 5; i++ {
		_, _ = fmt.Fprintf(&runnerLog, "runner-%d\n", i)
		_, _ = fmt.Fprintf(&gooseLog, "goose-%d\n", i)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte(runnerLog.String()), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(gooseLog.String()), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=2", nil)
	rec := httptest.NewRecorder()
	s.HandleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	body := rec.Body.String()
	if strings.Contains(body, "runner-1") || strings.Contains(body, "goose-1") {
		t.Fatalf("expected oldest lines to be omitted, got:\n%s", body)
	}
	if !strings.Contains(body, "runner-5") || !strings.Contains(body, "goose-5") {
		t.Fatalf("expected newest lines to be present, got:\n%s", body)
	}
}

func TestHandleRunLogsJSONIncludesStatusAndDone(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_logs_json",
		TaskID:      "task_logs_json",
		Repo:        "owner/repo",
		Instruction: "show logs as json",
		BaseBranch:  "main",
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	markRunSucceeded(t, s, run.ID)

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\nrunner-2\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte("goose-1\ngoose-2\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=1&format=json", nil)
	rec := httptest.NewRecorder()
	s.HandleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}
	if !strings.Contains(rec.Header().Get("Content-Type"), "application/json") {
		t.Fatalf("expected json content type, got %q", rec.Header().Get("Content-Type"))
	}
	var out struct {
		Logs      string          `json:"logs"`
		RunStatus state.RunStatus `json:"run_status"`
		Done      bool            `json:"done"`
	}
	if err := json.NewDecoder(rec.Body).Decode(&out); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if out.RunStatus != state.StatusSucceeded {
		t.Fatalf("expected succeeded status, got %s", out.RunStatus)
	}
	if !out.Done {
		t.Fatal("expected done=true for succeeded run")
	}
	if strings.Contains(out.Logs, "runner-1") || strings.Contains(out.Logs, "goose-1") {
		t.Fatalf("expected oldest lines to be omitted, got:\n%s", out.Logs)
	}
	if !strings.Contains(out.Logs, "runner-2") || !strings.Contains(out.Logs, "goose-2") {
		t.Fatalf("expected newest lines to be present, got:\n%s", out.Logs)
	}
}

func TestHandleRunLogsMissingAgentFileStillReturnsRunnerLogs(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_logs_missing_goose",
		TaskID:      "task_logs_missing_goose",
		Repo:        "owner/repo",
		Instruction: "show logs without agent output",
		BaseBranch:  "main",
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\nrunner-2\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=5", nil)
	rec := httptest.NewRecorder()
	s.HandleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "runner-2") {
		t.Fatalf("expected runner logs in response, got:\n%s", body)
	}
	if !strings.Contains(body, "== agent.ndjson ==") {
		t.Fatalf("expected agent section header in response, got:\n%s", body)
	}
	if !strings.Contains(body, "(agent.ndjson not found)") {
		t.Fatalf("expected missing agent note, got:\n%s", body)
	}
}

func TestHandleRunLogsFallsBackToLegacyGooseLogFile(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_logs_legacy_goose",
		TaskID:      "task_logs_legacy_goose",
		Repo:        "owner/repo",
		Instruction: "show logs from legacy file",
		BaseBranch:  "main",
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "goose.ndjson"), []byte("legacy-1\nlegacy-2\n"), 0o644); err != nil {
		t.Fatalf("write legacy goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?lines=5", nil)
	rec := httptest.NewRecorder()
	s.HandleRunSubresources(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", rec.Code)
	}

	body := rec.Body.String()
	if !strings.Contains(body, "== agent.ndjson ==") {
		t.Fatalf("expected agent section header in response, got:\n%s", body)
	}
	if !strings.Contains(body, "legacy-2") {
		t.Fatalf("expected legacy goose log contents, got:\n%s", body)
	}
	if strings.Contains(body, "(agent.ndjson not found)") {
		t.Fatalf("did not expect missing agent note when legacy file exists, got:\n%s", body)
	}
}

func TestHandleRunLogsRejectsInvalidFormat(t *testing.T) {
	t.Parallel()
	s := newTestServer(t, &fakeRunner{})
	defer waitForServerIdle(t, s)

	runDir := t.TempDir()
	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_logs_bad_format",
		TaskID:      "task_logs_bad_format",
		Repo:        "owner/repo",
		Instruction: "bad format",
		BaseBranch:  "main",
		RunDir:      runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "runner.log"), []byte("runner-1\n"), 0o644); err != nil {
		t.Fatalf("write runner log: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte("goose-1\n"), 0o644); err != nil {
		t.Fatalf("write goose log: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/v1/runs/"+run.ID+"/logs?format=xml", nil)
	rec := httptest.NewRecorder()
	s.HandleRunSubresources(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", rec.Code)
	}
}
