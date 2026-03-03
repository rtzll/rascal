package main

import (
	"strings"

	"github.com/spf13/cobra"
)

func (a *app) newGitHubCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "github",
		Short: "GitHub integration setup and status",
		Long:  "Configure and inspect GitHub repository integrations used by Rascal.",
		Example: strings.TrimSpace(`
rascal github setup OWNER/REPO --github-token "$GITHUB_TOKEN" --webhook-secret "$WEBHOOK_SECRET"
rascal github status OWNER/REPO --github-token "$GITHUB_TOKEN"
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	setupCmd := a.newRepoEnableCmd()
	setupCmd.Use = "setup [OWNER/REPO]"
	setupCmd.Short = "Ensure rascal label and webhook are configured"
	cmd.AddCommand(setupCmd)

	disableCmd := a.newRepoDisableCmd()
	disableCmd.Use = "disable [OWNER/REPO]"
	disableCmd.Short = "Remove Rascal webhook from a repository"
	cmd.AddCommand(disableCmd)

	statusCmd := a.newRepoStatusCmd()
	statusCmd.Use = "status [OWNER/REPO]"
	statusCmd.Short = "Show Rascal label/webhook status"
	cmd.AddCommand(statusCmd)

	return cmd
}
