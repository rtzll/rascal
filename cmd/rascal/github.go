package main

import (
	"os"
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub integration setup and status",
		Long:  "Configure and inspect GitHub repository integrations used by Rascal.",
		Example: strings.TrimSpace(`
rascal github setup OWNER/REPO --github-token "$GITHUB_TOKEN" --webhook-secret "$RASCAL_GITHUB_WEBHOOK_SECRET"
rascal github status OWNER/REPO --github-token "$GITHUB_TOKEN"
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	var githubAdminToken string
	setupCmd := &cobra.Command{
		Use:   "setup [OWNER/REPO]",
		Short: "Sync GitHub label/webhook using the registered repository record",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo := resolveRepoArg(args, a.cfg.DefaultRepo)
			if repo == "" {
				return &cliError{Code: exitInput, Message: "repo is required", Hint: "pass OWNER/REPO or set --default-repo"}
			}
			token := firstNonEmpty(strings.TrimSpace(githubAdminToken), strings.TrimSpace(os.Getenv("GITHUB_ADMIN_TOKEN")), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
			if token == "" {
				return &cliError{Code: exitInput, Message: "missing GitHub admin token", Hint: "use --github-admin-token or GITHUB_ADMIN_TOKEN"}
			}
			_, result, err := a.syncRepositoryGitHub(repo, token)
			if err != nil {
				return err
			}
			return a.emit(map[string]any{
				"repo":        repo,
				"enabled":     true,
				"webhook_url": result.WebhookURL,
				"label":       "rascal",
			}, func() error {
				a.println("repo enabled: %s", repo)
				a.println("webhook: %s", result.WebhookURL)
				return nil
			})
		},
	}
	setupCmd.Flags().StringVar(&githubAdminToken, "github-admin-token", "", "GitHub token with repo Webhooks (rw) and Issues (rw)")
	cmd.AddCommand(setupCmd)

	disableCmd := a.newRepoDisableCmd()
	disableCmd.Use = "disable [OWNER/REPO]"
	disableCmd.Short = "Remove Rascal webhook from a repository"
	cmd.AddCommand(disableCmd)

	statusCmd := a.newRepoStatusCmd()
	statusCmd.Use = "status [OWNER/REPO]"
	statusCmd.Short = "Show Rascal label/webhook status"
	cmd.AddCommand(statusCmd)

	cmd.AddCommand(a.newWebhookCmd())

	return cmd
}
