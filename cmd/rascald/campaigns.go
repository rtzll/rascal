package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/state"
)

func (s *server) handleCampaigns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		entries, err := s.listCampaignEntries()
		if err != nil {
			http.Error(w, "failed to list campaigns", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, api.CampaignListResponse{Campaigns: entries})
	case http.MethodPost:
		if s.isDraining() {
			http.Error(w, "server is draining", http.StatusServiceUnavailable)
			return
		}
		var req api.CreateCampaignRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			http.Error(w, "invalid JSON body", http.StatusBadRequest)
			return
		}
		campaign, _, err := s.store.CreateCampaign(state.CreateCampaignInput{
			ID:          req.ID,
			Name:        req.Name,
			Description: req.Description,
			Policy:      req.Policy,
			Items:       req.Items,
		})
		if err != nil {
			http.Error(w, "failed to create campaign: "+err.Error(), http.StatusBadRequest)
			return
		}
		view, err := s.buildCampaignView(campaign.ID, false)
		if err != nil {
			http.Error(w, "failed to load campaign", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusCreated, api.CampaignResponse{Campaign: view})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *server) handleCampaignSubresources(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/v1/campaigns/")
	path = strings.Trim(path, "/")
	if path == "" {
		http.Error(w, "campaign id is required", http.StatusBadRequest)
		return
	}
	parts := strings.Split(path, "/")
	campaignID := strings.TrimSpace(parts[0])
	if campaignID == "" {
		http.Error(w, "campaign id is required", http.StatusBadRequest)
		return
	}

	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
			return
		}
		view, err := s.buildCampaignView(campaignID, true)
		if err != nil {
			if errors.Is(err, errNotFound) {
				http.Error(w, "campaign not found", http.StatusNotFound)
				return
			}
			http.Error(w, "failed to load campaign", http.StatusInternalServerError)
			return
		}
		writeJSON(w, http.StatusOK, api.CampaignResponse{Campaign: view})
		return
	}

	if len(parts) != 2 || r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}

	var (
		view api.CampaignView
		err  error
	)
	switch parts[1] {
	case "start":
		view, err = s.startCampaign(campaignID)
	case "pause":
		view, err = s.pauseCampaign(campaignID)
	case "resume":
		view, err = s.resumeCampaign(campaignID)
	case "cancel":
		view, err = s.cancelCampaign(campaignID)
	case "retry-failed":
		view, err = s.retryFailedCampaignItems(campaignID)
	default:
		http.Error(w, "unknown campaign action", http.StatusNotFound)
		return
	}
	if err != nil {
		if errors.Is(err, errNotFound) {
			http.Error(w, "campaign not found", http.StatusNotFound)
			return
		}
		var cliErr *campaignActionError
		if errors.As(err, &cliErr) {
			http.Error(w, cliErr.Error(), http.StatusBadRequest)
			return
		}
		http.Error(w, "campaign action failed: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, api.CampaignResponse{Campaign: view})
}

var errNotFound = errors.New("not found")

type campaignActionError struct {
	message string
}

func (e *campaignActionError) Error() string {
	return e.message
}

func wrapCampaignErr(action string, err error) error {
	if err == nil {
		return nil
	}
	return fmt.Errorf("%s: %w", action, err)
}

func (s *server) listCampaignEntries() ([]api.CampaignListEntry, error) {
	if err := s.scheduleCampaigns(); err != nil {
		return nil, wrapCampaignErr("schedule campaigns before list", err)
	}
	campaigns, err := s.store.ListCampaigns()
	if err != nil {
		return nil, wrapCampaignErr("list campaigns", err)
	}
	out := make([]api.CampaignListEntry, 0, len(campaigns))
	for _, campaign := range campaigns {
		items, err := s.store.ListCampaignItems(campaign.ID)
		if err != nil {
			return nil, wrapCampaignErr("list campaign items", err)
		}
		out = append(out, api.CampaignListEntry{
			Campaign: campaign,
			Summary:  state.SummarizeCampaignItems(items),
		})
	}
	return out, nil
}

func (s *server) buildCampaignView(campaignID string, sync bool) (api.CampaignView, error) {
	if sync {
		if err := s.syncCampaign(campaignID); err != nil {
			return api.CampaignView{}, err
		}
	}
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	items, err := s.store.ListCampaignItems(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("list campaign items", err)
	}
	viewItems := make([]api.CampaignItemView, 0, len(items))
	for _, item := range items {
		itemView := api.CampaignItemView{Item: item}
		if strings.TrimSpace(item.RunID) != "" {
			if run, ok := s.store.GetRun(item.RunID); ok {
				runCopy := run
				itemView.Run = &runCopy
			}
		}
		viewItems = append(viewItems, itemView)
	}
	return api.CampaignView{
		Campaign: campaign,
		Summary:  state.SummarizeCampaignItems(items),
		Items:    viewItems,
	}, nil
}

func (s *server) startCampaign(campaignID string) (api.CampaignView, error) {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	if campaign.State != state.CampaignStateDraft {
		return api.CampaignView{}, &campaignActionError{message: "campaign start only supports draft campaigns"}
	}
	if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
		now := time.Now().UTC()
		c.State = state.CampaignStateRunning
		if c.StartedAt == nil {
			c.StartedAt = &now
		}
		c.CompletedAt = nil
		return nil
	}); err != nil {
		return api.CampaignView{}, wrapCampaignErr("start campaign", err)
	}
	if err := s.scheduleCampaigns(); err != nil {
		return api.CampaignView{}, wrapCampaignErr("schedule campaign start", err)
	}
	return s.buildCampaignView(campaignID, true)
}

func (s *server) pauseCampaign(campaignID string) (api.CampaignView, error) {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	if campaign.State != state.CampaignStateRunning {
		return api.CampaignView{}, &campaignActionError{message: "campaign pause only supports running campaigns"}
	}
	if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
		c.State = state.CampaignStatePaused
		return nil
	}); err != nil {
		return api.CampaignView{}, wrapCampaignErr("pause campaign", err)
	}
	return s.buildCampaignView(campaignID, true)
}

func (s *server) resumeCampaign(campaignID string) (api.CampaignView, error) {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	if campaign.State != state.CampaignStatePaused && campaign.State != state.CampaignStateFailed {
		return api.CampaignView{}, &campaignActionError{message: "campaign resume only supports paused or failed campaigns"}
	}
	if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
		now := time.Now().UTC()
		c.State = state.CampaignStateRunning
		if c.StartedAt == nil {
			c.StartedAt = &now
		}
		c.CompletedAt = nil
		return nil
	}); err != nil {
		return api.CampaignView{}, wrapCampaignErr("resume campaign", err)
	}
	if err := s.scheduleCampaigns(); err != nil {
		return api.CampaignView{}, wrapCampaignErr("schedule campaign resume", err)
	}
	return s.buildCampaignView(campaignID, true)
}

func (s *server) cancelCampaign(campaignID string) (api.CampaignView, error) {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	if campaign.State == state.CampaignStateCompleted || campaign.State == state.CampaignStateCanceled {
		return api.CampaignView{}, &campaignActionError{message: "campaign cancel does not support completed or canceled campaigns"}
	}
	items, err := s.store.ListCampaignItems(campaign.ID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("list campaign items", err)
	}
	for _, item := range items {
		switch item.State {
		case state.CampaignItemStatePending:
			if _, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
				ci.State = state.CampaignItemStateCanceled
				ci.SkipReason = "campaign canceled"
				ci.FailureReason = ""
				return nil
			}); err != nil {
				return api.CampaignView{}, wrapCampaignErr("mark pending campaign item canceled", err)
			}
		case state.CampaignItemStateQueued, state.CampaignItemStateRunning:
			if err := s.store.RequestRunCancel(item.RunID, "campaign canceled", "campaign"); err != nil {
				return api.CampaignView{}, wrapCampaignErr("request run cancel", err)
			}
			if run, ok := s.store.GetRun(item.RunID); ok && run.Status == state.StatusQueued {
				if _, err := s.store.SetRunStatus(item.RunID, state.StatusCanceled, "campaign canceled"); err != nil {
					return api.CampaignView{}, wrapCampaignErr("cancel queued run", err)
				}
			} else {
				s.stopRunExecutionBestEffort(item.RunID, "campaign cancellation")
			}
		}
	}
	if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
		now := time.Now().UTC()
		c.State = state.CampaignStateCanceled
		c.CompletedAt = &now
		return nil
	}); err != nil {
		return api.CampaignView{}, wrapCampaignErr("cancel campaign", err)
	}
	return s.buildCampaignView(campaignID, true)
}

func (s *server) retryFailedCampaignItems(campaignID string) (api.CampaignView, error) {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("get campaign", err)
	}
	if !ok {
		return api.CampaignView{}, errNotFound
	}
	if campaign.State == state.CampaignStateCanceled {
		return api.CampaignView{}, &campaignActionError{message: "campaign retry-failed does not support canceled campaigns"}
	}
	items, err := s.store.ListCampaignItems(campaign.ID)
	if err != nil {
		return api.CampaignView{}, wrapCampaignErr("list campaign items", err)
	}
	resetCount := 0
	for _, item := range items {
		if item.State != state.CampaignItemStateFailed {
			continue
		}
		if _, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
			ci.State = state.CampaignItemStatePending
			ci.RunID = ""
			ci.SkipReason = ""
			ci.FailureReason = ""
			return nil
		}); err != nil {
			return api.CampaignView{}, wrapCampaignErr("reset failed campaign item", err)
		}
		resetCount++
	}
	if resetCount == 0 {
		return api.CampaignView{}, &campaignActionError{message: "campaign has no failed items to retry"}
	}
	if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
		now := time.Now().UTC()
		c.State = state.CampaignStateRunning
		if c.StartedAt == nil {
			c.StartedAt = &now
		}
		c.CompletedAt = nil
		return nil
	}); err != nil {
		return api.CampaignView{}, wrapCampaignErr("retry failed campaign items", err)
	}
	if err := s.scheduleCampaigns(); err != nil {
		return api.CampaignView{}, wrapCampaignErr("schedule campaign retry", err)
	}
	return s.buildCampaignView(campaignID, true)
}

func (s *server) scheduleCampaigns() error {
	campaigns, err := s.store.ListCampaigns()
	if err != nil {
		return wrapCampaignErr("list campaigns for scheduler", err)
	}
	for _, campaign := range campaigns {
		if err := s.syncCampaign(campaign.ID); err != nil {
			return err
		}
	}
	return nil
}

func (s *server) syncCampaign(campaignID string) error {
	campaign, ok, err := s.store.GetCampaign(campaignID)
	if err != nil {
		return wrapCampaignErr("get campaign for sync", err)
	}
	if !ok {
		return errNotFound
	}
	items, err := s.store.ListCampaignItems(campaignID)
	if err != nil {
		return wrapCampaignErr("list campaign items for sync", err)
	}

	changed := false
	for _, item := range items {
		_, itemChanged, err := s.syncCampaignItem(item)
		if err != nil {
			return wrapCampaignErr("sync campaign item", err)
		}
		if itemChanged {
			changed = true
		}
	}
	if changed {
		campaign, ok, err = s.store.GetCampaign(campaignID)
		if err != nil {
			return wrapCampaignErr("reload campaign", err)
		}
		if !ok {
			return errNotFound
		}
		items, err = s.store.ListCampaignItems(campaignID)
		if err != nil {
			return wrapCampaignErr("reload campaign items", err)
		}
	}

	summary := state.SummarizeCampaignItems(items)
	if campaign.State == state.CampaignStateRunning && campaignFailureThresholdReached(campaign, summary) {
		if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
			now := time.Now().UTC()
			c.State = state.CampaignStateFailed
			c.CompletedAt = &now
			return nil
		}); err != nil {
			return wrapCampaignErr("mark campaign failed", err)
		}
		campaign.State = state.CampaignStateFailed
	}

	if campaign.State == state.CampaignStateRunning {
		enqueued, err := s.enqueueCampaignItems(campaign, items)
		if err != nil {
			return wrapCampaignErr("enqueue campaign items", err)
		}
		if enqueued {
			campaign, ok, err = s.store.GetCampaign(campaignID)
			if err != nil {
				return wrapCampaignErr("reload campaign after enqueue", err)
			}
			if !ok {
				return errNotFound
			}
			items, err = s.store.ListCampaignItems(campaignID)
			if err != nil {
				return wrapCampaignErr("reload campaign items after enqueue", err)
			}
			summary = state.SummarizeCampaignItems(items)
			if campaignFailureThresholdReached(campaign, summary) {
				if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
					now := time.Now().UTC()
					c.State = state.CampaignStateFailed
					c.CompletedAt = &now
					return nil
				}); err != nil {
					return wrapCampaignErr("mark campaign failed after enqueue", err)
				}
				campaign.State = state.CampaignStateFailed
			}
		}
	}

	if campaign.State == state.CampaignStateRunning || campaign.State == state.CampaignStateFailed {
		if summary.Pending == 0 && summary.ActiveItems == 0 {
			nextState := state.CampaignStateCompleted
			if summary.Failed > 0 && !campaign.Policy.ContinueOnFailure {
				nextState = state.CampaignStateFailed
			}
			if _, err := s.store.UpdateCampaign(campaign.ID, func(c *state.Campaign) error {
				now := time.Now().UTC()
				c.State = nextState
				c.CompletedAt = &now
				return nil
			}); err != nil {
				return wrapCampaignErr("finalize campaign state", err)
			}
		}
	}
	return nil
}

func (s *server) syncCampaignItem(item state.CampaignItem) (state.CampaignItem, bool, error) {
	if item.State == state.CampaignItemStateSkipped {
		return item, false, nil
	}
	if strings.TrimSpace(item.RunID) == "" {
		return item, false, nil
	}
	run, ok := s.store.GetRun(item.RunID)
	if !ok {
		if item.State == state.CampaignItemStateQueued || item.State == state.CampaignItemStateRunning {
			updated, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
				ci.State = state.CampaignItemStateFailed
				ci.FailureReason = "linked run not found"
				return nil
			})
			return updated, true, wrapCampaignErr("mark missing linked run as failed", err)
		}
		return item, false, nil
	}
	nextState := state.CampaignItemStateFromRunStatus(run.Status)
	nextSkipReason := item.SkipReason
	nextFailureReason := item.FailureReason
	switch nextState {
	case state.CampaignItemStateQueued, state.CampaignItemStateRunning, state.CampaignItemStateReview, state.CampaignItemStateSucceeded:
		nextSkipReason = ""
		nextFailureReason = ""
	case state.CampaignItemStateFailed, state.CampaignItemStateCanceled:
		nextFailureReason = strings.TrimSpace(run.Error)
	}
	if item.State == nextState && item.TaskID == run.TaskID && item.RunID == run.ID && item.SkipReason == nextSkipReason && item.FailureReason == nextFailureReason {
		return item, false, nil
	}
	updated, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
		ci.State = nextState
		ci.TaskID = run.TaskID
		ci.RunID = run.ID
		ci.SkipReason = nextSkipReason
		ci.FailureReason = nextFailureReason
		return nil
	})
	return updated, true, wrapCampaignErr("sync campaign item state", err)
}

func (s *server) enqueueCampaignItems(campaign state.Campaign, items []state.CampaignItem) (bool, error) {
	summary := state.SummarizeCampaignItems(items)
	available := campaign.Policy.MaxConcurrent - summary.ActiveItems
	if available <= 0 {
		return false, nil
	}
	changed := false
	for _, item := range items {
		if available <= 0 {
			break
		}
		if item.State != state.CampaignItemStatePending {
			continue
		}
		if campaignFailureThresholdReached(campaign, state.SummarizeCampaignItems(items)) {
			break
		}
		if campaign.Policy.SkipIfOpenPR {
			if existing, ok := s.findOpenPRRun(item); ok {
				updated, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
					ci.State = state.CampaignItemStateSkipped
					ci.RunID = existing.ID
					ci.SkipReason = fmt.Sprintf("open Rascal PR already exists for run %s", existing.ID)
					ci.FailureReason = ""
					return nil
				})
				if err != nil {
					return changed, wrapCampaignErr("skip campaign item for open PR", err)
				}
				items[item.Order-1] = updated
				changed = true
				continue
			}
		}

		req := runRequest{
			TaskID:          item.TaskID,
			Repo:            item.Repo,
			Task:            item.Task,
			BaseBranch:      item.BaseBranch,
			Trigger:         "campaign",
			AgentBackend:    strings.TrimSpace(item.BackendOverride),
			CreatedByUserID: "system",
		}
		run, err := s.createAndQueueRun(req)
		if err != nil {
			nextState := state.CampaignItemStateFailed
			reason := err.Error()
			if errors.Is(err, errTaskCompleted) {
				nextState = state.CampaignItemStateSkipped
				reason = "task is already completed"
			}
			updated, updateErr := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
				ci.State = nextState
				if nextState == state.CampaignItemStateSkipped {
					ci.SkipReason = reason
					ci.FailureReason = ""
				} else {
					ci.FailureReason = reason
					ci.SkipReason = ""
				}
				return nil
			})
			if updateErr != nil {
				return changed, wrapCampaignErr("record campaign item enqueue failure", updateErr)
			}
			items[item.Order-1] = updated
			changed = true
			continue
		}

		updated, err := s.store.UpdateCampaignItem(item.ID, func(ci *state.CampaignItem) error {
			ci.State = state.CampaignItemStateQueued
			ci.TaskID = run.TaskID
			ci.RunID = run.ID
			ci.SkipReason = ""
			ci.FailureReason = ""
			return nil
		})
		if err != nil {
			return changed, wrapCampaignErr("record queued campaign item", err)
		}
		items[item.Order-1] = updated
		available--
		changed = true
	}
	return changed, nil
}

func campaignFailureThresholdReached(campaign state.Campaign, summary state.CampaignSummary) bool {
	if campaign.Policy.ContinueOnFailure {
		return false
	}
	if campaign.Policy.StopAfterFailures <= 0 {
		return false
	}
	return summary.Failed >= campaign.Policy.StopAfterFailures
}

func (s *server) findOpenPRRun(item state.CampaignItem) (state.Run, bool) {
	taskID := strings.TrimSpace(item.TaskID)
	task := strings.TrimSpace(item.Task)
	for _, run := range s.store.ListRuns(10000) {
		if !strings.EqualFold(run.Repo, item.Repo) {
			continue
		}
		if run.PRStatus != state.PRStatusOpen || run.PRNumber <= 0 {
			continue
		}
		if taskID != "" && run.TaskID == taskID {
			return run, true
		}
		if task != "" && strings.TrimSpace(run.Task) == task {
			return run, true
		}
	}
	return state.Run{}, false
}

func normalizeCampaignBackend(raw string, fallback agent.Backend) agent.Backend {
	raw = strings.TrimSpace(strings.ToLower(raw))
	if raw == "" {
		return fallback
	}
	return agent.NormalizeBackend(raw)
}
