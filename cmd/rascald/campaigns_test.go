package main

import (
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/state"
)

func TestCampaignPauseResumeFlow(t *testing.T) {
	t.Parallel()

	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)

	campaign, _, err := s.store.CreateCampaign(state.CreateCampaignInput{
		Name: "pause resume",
		Policy: state.CampaignExecutionPolicy{
			MaxConcurrent:     1,
			StopAfterFailures: 1,
			SkipIfOpenPR:      true,
		},
		Items: []state.CampaignItemInput{
			{Repo: "owner/repo", Task: "first item"},
			{Repo: "owner/repo", Task: "second item"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if _, err := s.startCampaign(campaign.ID); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "first campaign run to start")

	if _, err := s.pauseCampaign(campaign.ID); err != nil {
		t.Fatalf("pause campaign: %v", err)
	}

	close(waitCh)
	waitFor(t, 2*time.Second, func() bool {
		view, err := s.buildCampaignView(campaign.ID, true)
		if err != nil {
			return false
		}
		return view.Campaign.State == state.CampaignStatePaused &&
			view.Summary.Succeeded == 1 &&
			view.Summary.Pending == 1 &&
			launcher.Calls() == 1
	}, "campaign remains paused after active item completes")

	if _, err := s.resumeCampaign(campaign.ID); err != nil {
		t.Fatalf("resume campaign: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 2 }, "second campaign run to start after resume")
	waitFor(t, 2*time.Second, func() bool {
		view, err := s.buildCampaignView(campaign.ID, true)
		if err != nil {
			return false
		}
		return view.Campaign.State == state.CampaignStateCompleted &&
			view.Summary.Succeeded == 2
	}, "campaign completes after resume")
	waitForServerIdle(t, s)
}

func TestCampaignRetryFailedStopsAtThreshold(t *testing.T) {
	t.Parallel()

	launcher := &fakeLauncher{
		resSeq: []fakeRunResult{
			{ExitCode: 1, Error: "boom"},
			{},
			{},
		},
	}
	s := newTestServer(t, launcher)

	campaign, _, err := s.store.CreateCampaign(state.CreateCampaignInput{
		Name: "retry failed",
		Policy: state.CampaignExecutionPolicy{
			MaxConcurrent:     1,
			StopAfterFailures: 1,
			SkipIfOpenPR:      true,
		},
		Items: []state.CampaignItemInput{
			{Repo: "owner/repo", Task: "first item"},
			{Repo: "owner/repo", Task: "second item"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if _, err := s.startCampaign(campaign.ID); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		view, err := s.buildCampaignView(campaign.ID, true)
		if err != nil {
			return false
		}
		return view.Campaign.State == state.CampaignStateFailed &&
			view.Summary.Failed == 1 &&
			view.Summary.Pending == 1
	}, "campaign stops after reaching failure threshold")

	if _, err := s.retryFailedCampaignItems(campaign.ID); err != nil {
		t.Fatalf("retry failed campaign items: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		view, err := s.buildCampaignView(campaign.ID, true)
		if err != nil {
			return false
		}
		return view.Campaign.State == state.CampaignStateCompleted &&
			view.Summary.Succeeded == 2 &&
			view.Summary.Failed == 0
	}, "campaign completes after retrying failed item")
}

func TestCampaignCancelRequestsRunCancellation(t *testing.T) {
	t.Parallel()

	waitCh := make(chan struct{})
	launcher := &fakeLauncher{waitCh: waitCh}
	s := newTestServer(t, launcher)

	campaign, _, err := s.store.CreateCampaign(state.CreateCampaignInput{
		Name: "cancel campaign",
		Policy: state.CampaignExecutionPolicy{
			MaxConcurrent:     1,
			StopAfterFailures: 1,
			SkipIfOpenPR:      true,
		},
		Items: []state.CampaignItemInput{
			{Repo: "owner/repo", Task: "cancel me"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if _, err := s.startCampaign(campaign.ID); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	waitFor(t, time.Second, func() bool { return launcher.Calls() == 1 }, "campaign run to start before cancel")

	view, err := s.cancelCampaign(campaign.ID)
	if err != nil {
		t.Fatalf("cancel campaign: %v", err)
	}
	if view.Campaign.State != state.CampaignStateCanceled {
		t.Fatalf("campaign state = %s, want canceled", view.Campaign.State)
	}
	if len(view.Items) != 1 || strings.TrimSpace(view.Items[0].Item.RunID) == "" {
		t.Fatalf("expected canceled campaign item to retain run id, got %+v", view.Items)
	}
	if cancelReq, ok := s.store.GetRunCancel(view.Items[0].Item.RunID); !ok {
		t.Fatal("expected run cancel request to be persisted")
	} else if cancelReq.Source != "campaign" {
		t.Fatalf("cancel source = %q, want campaign", cancelReq.Source)
	}
	close(waitCh)
	waitForServerIdle(t, s)
}

func TestCampaignSkipIfOpenPRExists(t *testing.T) {
	t.Parallel()

	launcher := &fakeLauncher{}
	s := newTestServer(t, launcher)

	if _, err := s.store.UpsertTask(state.UpsertTaskInput{ID: "existing-task", Repo: "owner/repo"}); err != nil {
		t.Fatalf("upsert task: %v", err)
	}
	run, err := s.store.AddRun(state.CreateRunInput{
		ID:         "run_existing_pr",
		TaskID:     "existing-task",
		Repo:       "owner/repo",
		Task:       "Update dependencies",
		BaseBranch: "main",
		HeadBranch: "rascal/existing",
		RunDir:     "/tmp/run_existing_pr",
	})
	if err != nil {
		t.Fatalf("add existing run: %v", err)
	}
	if _, err := s.store.SetRunStatus(run.ID, state.StatusRunning, ""); err != nil {
		t.Fatalf("set existing run running: %v", err)
	}
	if _, err := s.store.UpdateRun(run.ID, func(r *state.Run) error {
		r.Status = state.StatusReview
		r.PRNumber = 42
		r.PRStatus = state.PRStatusOpen
		return nil
	}); err != nil {
		t.Fatalf("set existing run review: %v", err)
	}

	campaign, _, err := s.store.CreateCampaign(state.CreateCampaignInput{
		Name: "skip open pr",
		Policy: state.CampaignExecutionPolicy{
			MaxConcurrent:     1,
			StopAfterFailures: 1,
			SkipIfOpenPR:      true,
		},
		Items: []state.CampaignItemInput{
			{Repo: "owner/repo", Task: "Update dependencies"},
		},
	})
	if err != nil {
		t.Fatalf("create campaign: %v", err)
	}

	if _, err := s.startCampaign(campaign.ID); err != nil {
		t.Fatalf("start campaign: %v", err)
	}
	waitFor(t, 2*time.Second, func() bool {
		view, err := s.buildCampaignView(campaign.ID, true)
		if err != nil {
			return false
		}
		return view.Campaign.State == state.CampaignStateCompleted &&
			view.Summary.Skipped == 1 &&
			launcher.Calls() == 0
	}, "campaign item skipped because open PR already exists")
}
