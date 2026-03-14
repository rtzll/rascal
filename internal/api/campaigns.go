package api

import "github.com/rtzll/rascal/internal/state"

type CreateCampaignRequest struct {
	ID          string                        `json:"id,omitempty" toml:"id,omitempty"`
	Name        string                        `json:"name" toml:"name"`
	Description string                        `json:"description,omitempty" toml:"description,omitempty"`
	Policy      state.CampaignExecutionPolicy `json:"policy" toml:"policy"`
	Items       []state.CampaignItemInput     `json:"items" toml:"items"`
}

type CampaignItemView struct {
	Item state.CampaignItem `json:"item" toml:"item"`
	Run  *state.Run         `json:"run,omitempty" toml:"run,omitempty"`
}

type CampaignView struct {
	Campaign state.Campaign        `json:"campaign" toml:"campaign"`
	Summary  state.CampaignSummary `json:"summary" toml:"summary"`
	Items    []CampaignItemView    `json:"items" toml:"items"`
}

type CampaignResponse struct {
	Campaign CampaignView `json:"campaign" toml:"campaign"`
}

type CampaignListEntry struct {
	Campaign state.Campaign        `json:"campaign" toml:"campaign"`
	Summary  state.CampaignSummary `json:"summary" toml:"summary"`
}

type CampaignListResponse struct {
	Campaigns []CampaignListEntry `json:"campaigns" toml:"campaigns"`
}
