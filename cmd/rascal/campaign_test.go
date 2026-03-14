package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/state"
)

func TestLoadCampaignCreateRequestFromManifestAndInlineItems(t *testing.T) {
	t.Parallel()

	tmp := t.TempDir()
	manifestPath := filepath.Join(tmp, "campaign.yaml")
	if err := os.WriteFile(manifestPath, []byte(`
name: deps rollout
description: batch dependency updates
policy:
  max_concurrent: 2
  stop_after_failures: 3
  continue_on_failure: true
  skip_if_open_pr: false
items:
  - repo: owner/repo
    task: Update dependencies
    task_id: deps-owner-repo
    base_branch: main
    backend: goose
`), 0o644); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	a := &app{}
	req, skipSpecified, err := a.loadCampaignCreateRequest(manifestPath, []string{
		`{"repo":"owner/other","task":"Run formatter","base_branch":"develop"}`,
	})
	if err != nil {
		t.Fatalf("load campaign create request: %v", err)
	}
	if !skipSpecified {
		t.Fatal("expected skip_if_open_pr to be marked as explicitly set")
	}
	if req.Name != "deps rollout" {
		t.Fatalf("name = %q, want deps rollout", req.Name)
	}
	if req.Policy.MaxConcurrent != 2 {
		t.Fatalf("max_concurrent = %d, want 2", req.Policy.MaxConcurrent)
	}
	if req.Policy.StopAfterFailures != 3 {
		t.Fatalf("stop_after_failures = %d, want 3", req.Policy.StopAfterFailures)
	}
	if !req.Policy.ContinueOnFailure {
		t.Fatal("expected continue_on_failure to be true")
	}
	if req.Policy.SkipIfOpenPR {
		t.Fatal("expected skip_if_open_pr to remain false from manifest")
	}
	if len(req.Items) != 2 {
		t.Fatalf("expected 2 items, got %d", len(req.Items))
	}
	if req.Items[0].BackendOverride != "goose" {
		t.Fatalf("backend override = %q, want goose", req.Items[0].BackendOverride)
	}
	if req.Items[1].Repo != "owner/other" {
		t.Fatalf("second item repo = %q, want owner/other", req.Items[1].Repo)
	}
}

func TestApplyCampaignDefaultsUsesDefaultRepo(t *testing.T) {
	t.Parallel()

	a := &app{
		cfg: config.ClientConfig{
			DefaultRepo: "Owner/Repo",
		},
	}
	req := apiCreateCampaignRequestForTest()
	if err := a.applyCampaignDefaults(&req); err != nil {
		t.Fatalf("apply campaign defaults: %v", err)
	}
	if req.Policy.MaxConcurrent != 1 {
		t.Fatalf("max concurrent = %d, want 1", req.Policy.MaxConcurrent)
	}
	if req.Policy.StopAfterFailures != 1 {
		t.Fatalf("stop after failures = %d, want 1", req.Policy.StopAfterFailures)
	}
	if req.Items[0].Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", req.Items[0].Repo)
	}
}

func apiCreateCampaignRequestForTest() api.CreateCampaignRequest {
	return api.CreateCampaignRequest{
		Name: "defaults",
		Items: []state.CampaignItemInput{
			{Task: "Run formatter"},
		},
	}
}
