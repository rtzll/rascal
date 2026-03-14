package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/state"
	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"
)

type campaignManifest struct {
	ID          string                 `json:"id,omitempty" yaml:"id"`
	Name        string                 `json:"name" yaml:"name"`
	Description string                 `json:"description,omitempty" yaml:"description"`
	Policy      campaignManifestPolicy `json:"policy" yaml:"policy"`
	Items       []campaignManifestItem `json:"items" yaml:"items"`
}

type campaignManifestPolicy struct {
	MaxConcurrent     int   `json:"max_concurrent" yaml:"max_concurrent"`
	StopAfterFailures int   `json:"stop_after_failures" yaml:"stop_after_failures"`
	ContinueOnFailure bool  `json:"continue_on_failure" yaml:"continue_on_failure"`
	SkipIfOpenPR      *bool `json:"skip_if_open_pr,omitempty" yaml:"skip_if_open_pr"`
}

type campaignManifestItem struct {
	Repo       string `json:"repo" yaml:"repo"`
	Task       string `json:"task" yaml:"task"`
	TaskID     string `json:"task_id,omitempty" yaml:"task_id"`
	BaseBranch string `json:"base_branch,omitempty" yaml:"base_branch"`
	Backend    string `json:"backend,omitempty" yaml:"backend"`
}

func (a *app) newCampaignCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "campaign",
		Short: "Manage multi-run maintenance campaigns",
		Long:  "Create, inspect, and control bounded batches of Rascal runs.",
		Example: strings.TrimSpace(`
rascal campaign create --name "Deps rollout" --manifest ./campaign.yaml
rascal campaign list
rascal campaign view camp_abc123
rascal campaign start camp_abc123
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newCampaignCreateCmd())
	cmd.AddCommand(a.newCampaignListCmd())
	cmd.AddCommand(a.newCampaignViewCmd())
	cmd.AddCommand(a.newCampaignActionCmd("start", "Start a draft campaign"))
	cmd.AddCommand(a.newCampaignActionCmd("pause", "Pause a running campaign"))
	cmd.AddCommand(a.newCampaignActionCmd("resume", "Resume a paused or failed campaign"))
	cmd.AddCommand(a.newCampaignActionCmd("cancel", "Cancel a campaign and request cancellation for active items"))
	cmd.AddCommand(a.newCampaignActionCmd("retry-failed", "Reset failed items and requeue the campaign"))
	return cmd
}

func (a *app) newCampaignCreateCmd() *cobra.Command {
	var (
		manifestPath      string
		inlineItems       []string
		campaignID        string
		name              string
		description       string
		maxConcurrent     int
		stopAfterFailures int
		continueOnFailure bool
		skipIfOpenPR      bool
		skipIfOpenPRSet   bool
	)
	cmd := &cobra.Command{
		Use:   "create",
		Short: "Create a draft campaign",
		Long:  "Create a draft campaign from a manifest file and/or inline item definitions. Creation validates the batch without enqueueing runs.",
		Example: strings.TrimSpace(`
rascal campaign create --name "Deps rollout" --manifest ./campaign.yaml
rascal campaign create --name "Hygiene" --item '{"repo":"owner/repo","task":"Run formatter"}'
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			req, skipSpecifiedByManifest, err := a.loadCampaignCreateRequest(manifestPath, inlineItems)
			if err != nil {
				return err
			}
			maxConcurrentSet := cmd.Flags().Changed("max-concurrent")
			stopAfterFailuresSet := cmd.Flags().Changed("stop-after-failures")
			skipIfOpenPRSet = cmd.Flags().Changed("skip-if-open-pr")
			if strings.TrimSpace(campaignID) != "" {
				req.ID = strings.TrimSpace(campaignID)
			}
			if strings.TrimSpace(name) != "" {
				req.Name = strings.TrimSpace(name)
			}
			if strings.TrimSpace(description) != "" {
				req.Description = strings.TrimSpace(description)
			}
			if maxConcurrentSet && maxConcurrent > 0 {
				req.Policy.MaxConcurrent = maxConcurrent
			}
			if stopAfterFailuresSet {
				req.Policy.StopAfterFailures = stopAfterFailures
			}
			if continueOnFailure {
				req.Policy.ContinueOnFailure = true
			}
			if skipIfOpenPRSet {
				req.Policy.SkipIfOpenPR = skipIfOpenPR
			} else if !skipSpecifiedByManifest {
				req.Policy.SkipIfOpenPR = true
			}
			if strings.TrimSpace(req.Name) == "" {
				return &cliError{Code: exitInput, Message: "campaign name is required", Hint: "set --name or include name in the manifest"}
			}
			if len(req.Items) == 0 {
				return &cliError{Code: exitInput, Message: "campaign items are required", Hint: "set --manifest and/or --item"}
			}
			if err := a.applyCampaignDefaults(&req); err != nil {
				return err
			}
			resp, err := doJSON(a.client, http.MethodPost, "/v1/campaigns", req)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer closeWithLog("close create campaign response body", resp.Body)
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out api.CampaignResponse
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return emit(a, out, func() error {
				return renderCampaignViewTable(out.Campaign)
			})
		},
	}
	cmd.Flags().StringVar(&campaignID, "id", "", "campaign identifier override")
	cmd.Flags().StringVar(&name, "name", "", "campaign name")
	cmd.Flags().StringVar(&description, "description", "", "campaign description")
	cmd.Flags().StringVar(&manifestPath, "manifest", "", "path to a YAML or JSON campaign manifest")
	cmd.Flags().StringArrayVar(&inlineItems, "item", nil, "inline item definition as JSON or YAML object")
	cmd.Flags().IntVar(&maxConcurrent, "max-concurrent", 0, "max items to enqueue at once")
	cmd.Flags().IntVar(&stopAfterFailures, "stop-after-failures", 1, "stop enqueueing after this many failed items (0 disables)")
	cmd.Flags().BoolVar(&continueOnFailure, "continue-on-failure", false, "keep enqueueing items after failures")
	cmd.Flags().BoolVar(&skipIfOpenPR, "skip-if-open-pr", true, "skip items when an open Rascal PR already exists")
	return cmd
}

func (a *app) newCampaignListCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "list",
		Short: "List campaigns",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			out, err := a.fetchCampaigns()
			if err != nil {
				return err
			}
			return emit(a, out, func() error {
				tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				if _, err := fmt.Fprintln(tw, "ID\tSTATE\tTOTAL\tACTIVE\tSUCCEEDED\tFAILED\tSKIPPED\tCREATED\tNAME"); err != nil {
					return fmt.Errorf("write campaign table header: %w", err)
				}
				for _, entry := range out.Campaigns {
					if _, err := fmt.Fprintf(
						tw,
						"%s\t%s\t%d\t%d\t%d\t%d\t%d\t%s\t%s\n",
						entry.Campaign.ID,
						entry.Campaign.State,
						entry.Summary.TotalItems,
						entry.Summary.ActiveItems,
						entry.Summary.Succeeded,
						entry.Summary.Failed,
						entry.Summary.Skipped,
						entry.Campaign.CreatedAt.Format(time.RFC3339),
						entry.Campaign.Name,
					); err != nil {
						return fmt.Errorf("write campaign table row: %w", err)
					}
				}
				return tw.Flush()
			})
		},
	}
	return cmd
}

func (a *app) newCampaignViewCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "view <campaign_id>",
		Short: "Show campaign progress and item details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			out, err := a.fetchCampaign(args[0])
			if err != nil {
				return err
			}
			return emit(a, out, func() error {
				return renderCampaignViewTable(out.Campaign)
			})
		},
	}
	return cmd
}

func (a *app) newCampaignActionCmd(action, short string) *cobra.Command {
	cmd := &cobra.Command{
		Use:   action + " <campaign_id>",
		Short: short,
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			out, err := a.campaignAction(args[0], action)
			if err != nil {
				return err
			}
			return emit(a, out, func() error {
				return renderCampaignViewTable(out.Campaign)
			})
		},
	}
	return cmd
}

func (a *app) loadCampaignCreateRequest(manifestPath string, inlineItems []string) (api.CreateCampaignRequest, bool, error) {
	req := api.CreateCampaignRequest{}
	skipSpecified := false
	if strings.TrimSpace(manifestPath) != "" {
		data, err := os.ReadFile(strings.TrimSpace(manifestPath))
		if err != nil {
			return req, false, &cliError{Code: exitInput, Message: "failed to read campaign manifest", Cause: err}
		}
		manifest, err := parseCampaignManifest(data)
		if err != nil {
			return req, false, err
		}
		req, skipSpecified = manifestToCreateCampaignRequest(manifest)
	}
	for _, raw := range inlineItems {
		manifestItem, err := parseCampaignManifestItem([]byte(raw))
		if err != nil {
			return req, false, err
		}
		req.Items = append(req.Items, state.CampaignItemInput{
			Repo:            manifestItem.Repo,
			Task:            manifestItem.Task,
			TaskID:          manifestItem.TaskID,
			BaseBranch:      manifestItem.BaseBranch,
			BackendOverride: manifestItem.Backend,
		})
	}
	return req, skipSpecified, nil
}

func (a *app) applyCampaignDefaults(req *api.CreateCampaignRequest) error {
	req.Policy = state.NormalizeCampaignExecutionPolicy(req.Policy)
	if req.Policy.MaxConcurrent == 0 {
		req.Policy.MaxConcurrent = 1
	}
	if req.Policy.StopAfterFailures == 0 && !req.Policy.ContinueOnFailure {
		req.Policy.StopAfterFailures = 1
	}
	for i := range req.Items {
		req.Items[i] = state.NormalizeCampaignItemInput(req.Items[i])
		if req.Items[i].Repo == "" {
			req.Items[i].Repo = state.NormalizeRepo(a.cfg.DefaultRepo)
		}
		if req.Items[i].Repo == "" || req.Items[i].Task == "" {
			return &cliError{Code: exitInput, Message: fmt.Sprintf("campaign item %d requires repo and task", i+1)}
		}
	}
	return nil
}

func parseCampaignManifest(data []byte) (campaignManifest, error) {
	var manifest campaignManifest
	if err := json.Unmarshal(data, &manifest); err == nil {
		return manifest, nil
	}
	if err := yaml.Unmarshal(data, &manifest); err != nil {
		return campaignManifest{}, &cliError{Code: exitInput, Message: "failed to parse campaign manifest", Cause: err}
	}
	return manifest, nil
}

func parseCampaignManifestItem(data []byte) (campaignManifestItem, error) {
	var item campaignManifestItem
	if err := json.Unmarshal(data, &item); err == nil {
		return item, nil
	}
	if err := yaml.Unmarshal(data, &item); err != nil {
		return campaignManifestItem{}, &cliError{Code: exitInput, Message: "failed to parse inline campaign item", Cause: err}
	}
	return item, nil
}

func manifestToCreateCampaignRequest(manifest campaignManifest) (api.CreateCampaignRequest, bool) {
	req := api.CreateCampaignRequest{
		ID:          manifest.ID,
		Name:        manifest.Name,
		Description: manifest.Description,
		Policy: state.CampaignExecutionPolicy{
			MaxConcurrent:     manifest.Policy.MaxConcurrent,
			StopAfterFailures: manifest.Policy.StopAfterFailures,
			ContinueOnFailure: manifest.Policy.ContinueOnFailure,
			SkipIfOpenPR:      true,
		},
	}
	skipSpecified := false
	if manifest.Policy.SkipIfOpenPR != nil {
		req.Policy.SkipIfOpenPR = *manifest.Policy.SkipIfOpenPR
		skipSpecified = true
	}
	for _, item := range manifest.Items {
		req.Items = append(req.Items, state.CampaignItemInput{
			Repo:            item.Repo,
			Task:            item.Task,
			TaskID:          item.TaskID,
			BaseBranch:      item.BaseBranch,
			BackendOverride: item.Backend,
		})
	}
	return req, skipSpecified
}

func (a *app) fetchCampaigns() (api.CampaignListResponse, error) {
	resp, err := a.client.do(http.MethodGet, "/v1/campaigns", nil)
	if err != nil {
		return api.CampaignListResponse{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close list campaigns response body", resp.Body)
	if resp.StatusCode >= 300 {
		return api.CampaignListResponse{}, decodeServerError(resp)
	}
	var out api.CampaignListResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.CampaignListResponse{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out, nil
}

func (a *app) fetchCampaign(campaignID string) (api.CampaignResponse, error) {
	resp, err := a.client.do(http.MethodGet, "/v1/campaigns/"+campaignID, nil)
	if err != nil {
		return api.CampaignResponse{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close get campaign response body", resp.Body)
	if resp.StatusCode >= 300 {
		return api.CampaignResponse{}, decodeServerError(resp)
	}
	var out api.CampaignResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.CampaignResponse{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out, nil
}

func (a *app) campaignAction(campaignID, action string) (api.CampaignResponse, error) {
	resp, err := a.client.do(http.MethodPost, "/v1/campaigns/"+campaignID+"/"+action, nil)
	if err != nil {
		return api.CampaignResponse{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer closeWithLog("close campaign action response body", resp.Body)
	if resp.StatusCode >= 300 {
		return api.CampaignResponse{}, decodeServerError(resp)
	}
	var out api.CampaignResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return api.CampaignResponse{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out, nil
}

func renderCampaignViewTable(view api.CampaignView) error {
	fmt.Printf("Campaign: %s\n", view.Campaign.ID)
	fmt.Printf("Name: %s\n", view.Campaign.Name)
	fmt.Printf("State: %s\n", view.Campaign.State)
	if strings.TrimSpace(view.Campaign.Description) != "" {
		fmt.Printf("Description: %s\n", view.Campaign.Description)
	}
	fmt.Printf(
		"Policy: max_concurrent=%d stop_after_failures=%d continue_on_failure=%t skip_if_open_pr=%t\n",
		view.Campaign.Policy.MaxConcurrent,
		view.Campaign.Policy.StopAfterFailures,
		view.Campaign.Policy.ContinueOnFailure,
		view.Campaign.Policy.SkipIfOpenPR,
	)
	fmt.Printf(
		"Summary: total=%d active=%d succeeded=%d failed=%d skipped=%d canceled=%d review=%d pending=%d queued=%d running=%d\n",
		view.Summary.TotalItems,
		view.Summary.ActiveItems,
		view.Summary.Succeeded,
		view.Summary.Failed,
		view.Summary.Skipped,
		view.Summary.Canceled,
		view.Summary.Review,
		view.Summary.Pending,
		view.Summary.Queued,
		view.Summary.Running,
	)

	tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
	if _, err := fmt.Fprintln(tw, "ORDER\tSTATE\tREPO\tRUN\tPR\tREASON\tTASK"); err != nil {
		return fmt.Errorf("write campaign view header: %w", err)
	}
	for _, item := range view.Items {
		reason := strings.TrimSpace(item.Item.SkipReason)
		if reason == "" {
			reason = strings.TrimSpace(item.Item.FailureReason)
		}
		prLabel := "-"
		runID := strings.TrimSpace(item.Item.RunID)
		if item.Run != nil {
			runID = item.Run.ID
			if item.Run.PRNumber > 0 {
				prLabel = fmt.Sprintf("#%d", item.Run.PRNumber)
			}
		}
		if runID == "" {
			runID = "-"
		}
		if _, err := fmt.Fprintf(
			tw,
			"%d\t%s\t%s\t%s\t%s\t%s\t%s\n",
			item.Item.Order,
			item.Item.State,
			item.Item.Repo,
			runID,
			prLabel,
			reason,
			item.Item.Task,
		); err != nil {
			return fmt.Errorf("write campaign view row: %w", err)
		}
	}
	if err := tw.Flush(); err != nil {
		return fmt.Errorf("flush campaign view table: %w", err)
	}
	return nil
}
