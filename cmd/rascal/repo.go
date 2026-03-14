package main

import (
	"context"
	"fmt"
	"os"
	"slices"
	"strings"
	"time"

	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/spf13/cobra"
)

var requiredWebhookEvents = []string{
	"issues",
	"issue_comment",
	"pull_request_review",
	"pull_request_review_comment",
	"pull_request_review_thread",
	"pull_request",
}

type repoEnableInput struct {
	Repo                       string
	GitHubToken                string
	WebhookSecret              string
	UseServerWebhookSecret     bool
	ResolveServerWebhookSecret func() (string, error)
	WebhookURL                 string
	Timeout                    time.Duration
	Client                     repoGitHubClient
	RawErrors                  bool
}

type repoEnableResult struct {
	Repo       string
	WebhookURL string
}

type repoEnableOutput struct {
	Repo       string `json:"repo"`
	Enabled    bool   `json:"enabled"`
	WebhookURL string `json:"webhook_url"`
	Label      string `json:"label"`
}

type repoDisableOutput struct {
	Repo       string `json:"repo"`
	Removed    bool   `json:"removed"`
	WebhookURL string `json:"webhook_url"`
}

type repoStatusOutput struct {
	Repo                 string             `json:"repo"`
	LabelExists          bool               `json:"label_exists"`
	WebhookURL           string             `json:"webhook_url"`
	Webhook              *ghapi.WebhookData `json:"webhook,omitempty"`
	RequiredEvents       []string           `json:"required_events"`
	MissingEvents        []string           `json:"missing_events"`
	WebhookEventsHealthy bool               `json:"webhook_events_healthy"`
}

type repoGitHubClient interface {
	EnsureLabel(ctx context.Context, repo, name, color, description string) error
	UpsertWebhook(ctx context.Context, repo, webhookURL, secret string, events []string) error
}

func (a *app) runRepoEnable(input repoEnableInput) (repoEnableResult, error) {
	repo := strings.TrimSpace(input.Repo)
	if repo == "" {
		return repoEnableResult{}, &cliError{Code: exitInput, Message: "repo is required", Hint: "pass OWNER/REPO or set --default-repo"}
	}
	githubToken := firstNonEmpty(strings.TrimSpace(input.GitHubToken), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
	if githubToken == "" {
		return repoEnableResult{}, &cliError{Code: exitInput, Message: "missing GitHub token", Hint: "set --github-token or GITHUB_TOKEN"}
	}
	webhookSecret := strings.TrimSpace(input.WebhookSecret)
	if webhookSecret == "" {
		webhookSecret = strings.TrimSpace(os.Getenv("RASCAL_GITHUB_WEBHOOK_SECRET"))
	}
	if webhookSecret == "" && input.UseServerWebhookSecret {
		resolveSecret := input.ResolveServerWebhookSecret
		if resolveSecret == nil {
			resolveSecret = a.fetchServerWebhookSecret
		}
		secret, err := resolveSecret()
		if err != nil {
			if input.RawErrors {
				return repoEnableResult{}, fmt.Errorf("resolve webhook secret from server: %w", err)
			}
			return repoEnableResult{}, &cliError{
				Code:    exitRuntime,
				Message: "failed to resolve webhook secret from server",
				Hint:    "check SSH access and remote /etc/rascal/rascal.env, or pass --webhook-secret explicitly",
				Cause:   err,
			}
		}
		webhookSecret = strings.TrimSpace(secret)
	}
	if webhookSecret == "" {
		return repoEnableResult{}, &cliError{
			Code:    exitInput,
			Message: "missing webhook secret",
			Hint:    "pass --webhook-secret, set RASCAL_GITHUB_WEBHOOK_SECRET, or use --use-server-webhook-secret",
		}
	}
	webhookURL := firstNonEmpty(strings.TrimSpace(input.WebhookURL), strings.TrimSpace(a.cfg.ServerURL)+"/v1/webhooks/github")
	webhookURL = strings.TrimRight(webhookURL, "/")
	timeout := input.Timeout
	if timeout <= 0 {
		timeout = 45 * time.Second
	}

	client := input.Client
	if client == nil {
		client = ghapi.NewAPIClient(githubToken)
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	if err := client.EnsureLabel(ctx, repo, "rascal", "0e8a16", "Trigger Rascal automation"); err != nil {
		if input.RawErrors {
			return repoEnableResult{}, fmt.Errorf("ensure label: %w", err)
		}
		return repoEnableResult{}, &cliError{Code: exitRuntime, Message: "failed to ensure label", Cause: err}
	}
	if err := client.UpsertWebhook(ctx, repo, webhookURL, webhookSecret, nil); err != nil {
		if input.RawErrors {
			return repoEnableResult{}, fmt.Errorf("upsert webhook: %w", err)
		}
		return repoEnableResult{}, &cliError{Code: exitRuntime, Message: "failed to upsert webhook", Cause: err}
	}
	return repoEnableResult{Repo: repo, WebhookURL: webhookURL}, nil
}

func (a *app) newRepoEnableCmd() *cobra.Command {
	var (
		githubToken            string
		webhookSecret          string
		useServerWebhookSecret bool
		webhookURL             string
		timeout                time.Duration
	)
	cmd := &cobra.Command{
		Use:   "enable [OWNER/REPO]",
		Short: "Ensure rascal label and webhook are configured",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := resolveRepoArg(args, a.cfg.DefaultRepo)
			result, err := a.runRepoEnable(repoEnableInput{
				Repo:                   repo,
				GitHubToken:            githubToken,
				WebhookSecret:          webhookSecret,
				UseServerWebhookSecret: useServerWebhookSecret,
				WebhookURL:             webhookURL,
				Timeout:                timeout,
			})
			if err != nil {
				return err
			}
			return a.emit(repoEnableOutput{
				Repo:       result.Repo,
				Enabled:    true,
				WebhookURL: result.WebhookURL,
				Label:      "rascal",
			}, func() error {
				a.println("repo enabled: %s", result.Repo)
				a.println("webhook: %s", result.WebhookURL)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token (or GITHUB_TOKEN)")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret (must match server secret)")
	cmd.Flags().BoolVar(&useServerWebhookSecret, "use-server-webhook-secret", false, "resolve webhook secret from remote /etc/rascal/rascal.env over SSH")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <orchestrator-url>/v1/webhooks/github)")
	cmd.Flags().DurationVar(&timeout, "timeout", 45*time.Second, "GitHub API timeout")
	return cmd
}

func (a *app) fetchServerWebhookSecret() (string, error) {
	cfg, err := a.resolveSSHConfig("", "", "", 0)
	if err != nil {
		return "", err
	}
	remoteCmd := strings.Join([]string{
		"set -euo pipefail",
		`line=$(grep -m1 -E '^RASCAL_GITHUB_WEBHOOK_SECRET=' /etc/rascal/rascal.env || true)`,
		`if [ -z "$line" ]; then`,
		`  echo "webhook secret not found in /etc/rascal/rascal.env" >&2`,
		`  exit 1`,
		`fi`,
		`val="${line#*=}"`,
		`val="${val%\"}"`,
		`val="${val#\"}"`,
		`printf '%s' "$val"`,
	}, "\n")

	out, err := runLocalCapture("ssh", sshArgs(cfg, remoteCmd)...)
	if err != nil {
		return "", err
	}
	out = strings.TrimSpace(out)
	if out == "" {
		return "", fmt.Errorf("remote webhook secret is empty")
	}
	return out, nil
}

func (a *app) newRepoDisableCmd() *cobra.Command {
	var (
		githubToken string
		webhookURL  string
		timeout     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "disable [OWNER/REPO]",
		Short: "Remove Rascal webhook from a repository",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := resolveRepoArg(args, a.cfg.DefaultRepo)
			if repo == "" {
				return &cliError{Code: exitInput, Message: "repo is required", Hint: "pass OWNER/REPO or set --default-repo"}
			}
			githubToken = firstNonEmpty(strings.TrimSpace(githubToken), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
			if githubToken == "" {
				return &cliError{Code: exitInput, Message: "missing GitHub token", Hint: "set --github-token or GITHUB_TOKEN"}
			}
			webhookURL = firstNonEmpty(strings.TrimSpace(webhookURL), strings.TrimSpace(a.cfg.ServerURL)+"/v1/webhooks/github")
			webhookURL = strings.TrimRight(webhookURL, "/")
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			gh := ghapi.NewAPIClient(githubToken)
			removed, err := gh.DeleteWebhookByURL(ctx, repo, webhookURL)
			if err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to remove webhook", Cause: err}
			}
			return a.emit(repoDisableOutput{
				Repo:       repo,
				Removed:    removed,
				WebhookURL: webhookURL,
			}, func() error {
				if removed {
					a.println("removed webhook for %s: %s", repo, webhookURL)
				} else {
					a.println("no matching webhook for %s: %s", repo, webhookURL)
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token (or GITHUB_TOKEN)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <orchestrator-url>/v1/webhooks/github)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "GitHub API timeout")
	return cmd
}

func (a *app) newRepoStatusCmd() *cobra.Command {
	var (
		githubToken string
		webhookURL  string
		timeout     time.Duration
	)
	cmd := &cobra.Command{
		Use:   "status [OWNER/REPO]",
		Short: "Show Rascal label/webhook status",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo := resolveRepoArg(args, a.cfg.DefaultRepo)
			if repo == "" {
				return &cliError{Code: exitInput, Message: "repo is required", Hint: "pass OWNER/REPO or set --default-repo"}
			}
			githubToken = firstNonEmpty(strings.TrimSpace(githubToken), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
			if githubToken == "" {
				return &cliError{Code: exitInput, Message: "missing GitHub token", Hint: "set --github-token or GITHUB_TOKEN"}
			}
			webhookURL = firstNonEmpty(strings.TrimSpace(webhookURL), strings.TrimSpace(a.cfg.ServerURL)+"/v1/webhooks/github")
			webhookURL = strings.TrimRight(webhookURL, "/")
			if timeout <= 0 {
				timeout = 30 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()
			gh := ghapi.NewAPIClient(githubToken)
			labelExists, err := gh.LabelExists(ctx, repo, "rascal")
			if err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to check label", Cause: err}
			}
			hook, err := gh.FindWebhookByURL(ctx, repo, webhookURL)
			if err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to check webhook", Cause: err}
			}
			missingEvents := []string{}
			if hook != nil {
				missingEvents = missingRequiredWebhookEvents(hook.Events)
			}
			out := repoStatusOutput{
				Repo:                 repo,
				LabelExists:          labelExists,
				WebhookURL:           webhookURL,
				Webhook:              hook,
				RequiredEvents:       requiredWebhookEvents,
				MissingEvents:        missingEvents,
				WebhookEventsHealthy: hook != nil && len(missingEvents) == 0,
			}
			return a.emit(out, func() error {
				a.println("repo: %s", repo)
				a.println("label rascal: %t", labelExists)
				if hook == nil {
					a.println("webhook: missing")
					return nil
				}
				a.println("webhook: id=%d active=%t url=%s", hook.ID, hook.Active, hook.URL)
				if len(hook.Events) > 0 {
					a.println("events: %s", strings.Join(hook.Events, ","))
				}
				if len(missingEvents) > 0 {
					a.println("warning: webhook missing required events: %s", strings.Join(missingEvents, ","))
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token (or GITHUB_TOKEN)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <orchestrator-url>/v1/webhooks/github)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "GitHub API timeout")
	return cmd
}

func resolveRepoArg(args []string, def string) string {
	if len(args) > 0 {
		return strings.TrimSpace(args[0])
	}
	return strings.TrimSpace(def)
}

func missingRequiredWebhookEvents(events []string) []string {
	normalized := make([]string, 0, len(events))
	for _, event := range events {
		event = strings.ToLower(strings.TrimSpace(event))
		if event == "" || slices.Contains(normalized, event) {
			continue
		}
		normalized = append(normalized, event)
	}

	missing := make([]string, 0)
	for _, want := range requiredWebhookEvents {
		if !slices.Contains(normalized, want) {
			missing = append(missing, want)
		}
	}
	return missing
}
