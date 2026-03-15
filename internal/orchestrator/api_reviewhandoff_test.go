package orchestrator

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/reviewhandoff"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

func TestHandleGetRunIncludesReviewHandoff(t *testing.T) {
	statePath := filepath.Join(t.TempDir(), "state.db")
	store, err := state.New(statePath, 20)
	if err != nil {
		t.Fatalf("open state store: %v", err)
	}
	t.Cleanup(func() {
		if err := store.Close(); err != nil {
			t.Fatalf("close state store: %v", err)
		}
	})

	runDir := filepath.Join(t.TempDir(), "run")
	if err := reviewhandoff.WriteArtifacts(runDir, reviewhandoff.Analyze(reviewhandoff.Input{
		BaseRef: "main",
		HeadRef: "HEAD",
		ChangedFiles: []reviewhandoff.ChangedFile{
			{Path: "internal/worker/worker.go"},
			{Path: "internal/worker/worker_test.go"},
		},
	})); err != nil {
		t.Fatalf("write review handoff: %v", err)
	}

	run, err := store.AddRun(state.CreateRunInput{
		ID:           "run_review_handoff",
		TaskID:       "task_review_handoff",
		Repo:         "owner/repo",
		Instruction:  "Add review handoff",
		AgentRuntime: runtime.RuntimeGooseCodex,
		BaseBranch:   "main",
		HeadBranch:   "rascal/run_review_handoff",
		RunDir:       runDir,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}

	srv := NewServer(config.ServerConfig{}, store, nil, nil, nil, nil, "test-instance")
	rec := httptest.NewRecorder()
	srv.HandleGetRun(rec, run.ID)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp api.RunResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if resp.ReviewHandoff == nil {
		t.Fatalf("expected review handoff payload, got nil")
	}
	if resp.ReviewHandoff.Risk.Level == "" {
		t.Fatalf("expected risk level in payload, got %#v", resp.ReviewHandoff)
	}
}
