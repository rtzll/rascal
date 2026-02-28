package main

import (
	"context"
	"os"
	"strings"
	"time"

	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/spf13/cobra"
)

func (a *app) newRepoCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "repo",
		Short: "Repository webhook/label operations",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newRepoEnableCmd())
	cmd.AddCommand(a.newRepoDisableCmd())
	cmd.AddCommand(a.newRepoStatusCmd())
	return cmd
}

func (a *app) newRepoEnableCmd() *cobra.Command {
	var (
		githubToken   string
		webhookSecret string
		webhookURL    string
		timeout       time.Duration
	)
	cmd := &cobra.Command{
		Use:   "enable [OWNER/REPO]",
		Short: "Ensure rascal label and webhook are configured",
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
			webhookSecret = strings.TrimSpace(webhookSecret)
			if webhookSecret == "" {
				return &cliError{Code: exitInput, Message: "missing webhook secret", Hint: "pass --webhook-secret (must match server secret)"}
			}
			webhookURL = firstNonEmpty(strings.TrimSpace(webhookURL), strings.TrimSpace(a.cfg.ServerURL)+"/v1/webhooks/github")
			webhookURL = strings.TrimRight(webhookURL, "/")
			if timeout <= 0 {
				timeout = 45 * time.Second
			}

			gh := ghapi.NewAPIClient(githubToken)
			ctx, cancel := context.WithTimeout(context.Background(), timeout)
			defer cancel()

			if err := gh.EnsureLabel(ctx, repo, "rascal", "0e8a16", "Trigger Rascal automation"); err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to ensure label", Cause: err}
			}
			if err := gh.UpsertWebhook(ctx, repo, webhookURL, webhookSecret, nil); err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to upsert webhook", Cause: err}
			}
			return a.emit(map[string]any{
				"repo":        repo,
				"enabled":     true,
				"webhook_url": webhookURL,
				"label":       "rascal",
			}, func() error {
				a.println("repo enabled: %s", repo)
				a.println("webhook: %s", webhookURL)
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token (or GITHUB_TOKEN)")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret (must match server secret)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <server-url>/v1/webhooks/github)")
	cmd.Flags().DurationVar(&timeout, "timeout", 45*time.Second, "GitHub API timeout")
	return cmd
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
			return a.emit(map[string]any{
				"repo":        repo,
				"removed":     removed,
				"webhook_url": webhookURL,
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
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <server-url>/v1/webhooks/github)")
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
			out := map[string]any{
				"repo":         repo,
				"label_exists": labelExists,
				"webhook_url":  webhookURL,
				"webhook":      hook,
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
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token (or GITHUB_TOKEN)")
	cmd.Flags().StringVar(&webhookURL, "webhook-url", "", "override webhook URL (default: <server-url>/v1/webhooks/github)")
	cmd.Flags().DurationVar(&timeout, "timeout", 30*time.Second, "GitHub API timeout")
	return cmd
}

func resolveRepoArg(args []string, def string) string {
	if len(args) > 0 {
		return strings.TrimSpace(args[0])
	}
	return strings.TrimSpace(def)
}
