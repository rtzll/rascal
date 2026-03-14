package state

import (
	"path/filepath"
	"testing"
)

func TestStoreCreateCampaignAssignsDefaults(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	campaign, items, err := store.CreateCampaign(CreateCampaignInput{
		Name: "deps rollout",
		Items: []CampaignItemInput{
			{Repo: "Owner/Repo", Task: "Update dependencies"},
			{Repo: "owner/other", Task: "Run formatter", TaskID: "fmt-task"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if campaign.State != CampaignStateDraft {
		t.Fatalf("campaign state = %s, want draft", campaign.State)
	}
	if campaign.Policy.MaxConcurrent != 1 {
		t.Fatalf("max concurrent = %d, want 1", campaign.Policy.MaxConcurrent)
	}
	if campaign.Policy.StopAfterFailures != 1 {
		t.Fatalf("stop after failures = %d, want 1", campaign.Policy.StopAfterFailures)
	}
	if !campaign.Policy.SkipIfOpenPR {
		t.Fatal("expected skip_if_open_pr to default to true")
	}
	if len(items) != 2 {
		t.Fatalf("expected 2 campaign items, got %d", len(items))
	}
	if items[0].Repo != "owner/repo" {
		t.Fatalf("first item repo = %q, want owner/repo", items[0].Repo)
	}
	if items[0].TaskID == "" {
		t.Fatal("expected first item task id to be assigned")
	}
	if items[1].TaskID != "fmt-task" {
		t.Fatalf("second item task id = %q, want fmt-task", items[1].TaskID)
	}

	loaded, ok, err := store.GetCampaign(campaign.ID)
	if err != nil {
		t.Fatalf("get campaign: %v", err)
	}
	if !ok {
		t.Fatal("expected campaign to exist")
	}
	if loaded.Name != campaign.Name {
		t.Fatalf("loaded name = %q, want %q", loaded.Name, campaign.Name)
	}

	loadedItems, err := store.ListCampaignItems(campaign.ID)
	if err != nil {
		t.Fatalf("list campaign items: %v", err)
	}
	if len(loadedItems) != 2 {
		t.Fatalf("expected 2 persisted campaign items, got %d", len(loadedItems))
	}
}

func TestStoreCampaignUpdateValidatesTransitions(t *testing.T) {
	t.Parallel()

	store, err := New(filepath.Join(t.TempDir(), "state.db"), 200)
	if err != nil {
		t.Fatalf("new store: %v", err)
	}

	campaign, items, err := store.CreateCampaign(CreateCampaignInput{
		Name: "transition test",
		Items: []CampaignItemInput{
			{Repo: "owner/repo", Task: "Do work"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if _, err := store.UpdateCampaign(campaign.ID, func(c *Campaign) error {
		c.State = CampaignStateRunning
		return nil
	}); err != nil {
		t.Fatalf("set campaign running: %v", err)
	}
	if _, err := store.UpdateCampaign(campaign.ID, func(c *Campaign) error {
		c.State = CampaignStateDraft
		return nil
	}); err == nil {
		t.Fatal("expected invalid campaign transition to fail")
	}

	if _, err := store.UpdateCampaignItem(items[0].ID, func(item *CampaignItem) error {
		item.State = CampaignItemStateQueued
		return nil
	}); err != nil {
		t.Fatalf("set campaign item queued: %v", err)
	}
	if _, err := store.UpdateCampaignItem(items[0].ID, func(item *CampaignItem) error {
		item.State = CampaignItemStatePending
		return nil
	}); err == nil {
		t.Fatal("expected invalid campaign item transition to fail")
	}
}
