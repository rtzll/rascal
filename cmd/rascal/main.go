package main

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
	"go.yaml.in/yaml/v3"
)

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

type app struct {
	configPath      string
	serverURLFlag   string
	apiTokenFlag    string
	defaultRepoFlag string
	output          string
	noColor         bool
	quiet           bool
	cfg             config.ClientConfig
	client          apiClient
	serverSource    string
	tokenSource     string
	repoSource      string
}

type cliError struct {
	Code      int
	Message   string
	Hint      string
	RequestID string
	Cause     error
}

func (e *cliError) Error() string {
	if e == nil {
		return ""
	}
	if strings.TrimSpace(e.Message) != "" {
		return e.Message
	}
	if e.Cause != nil {
		return e.Cause.Error()
	}
	return "unknown error"
}

func (e *cliError) Unwrap() error { return e.Cause }

const (
	exitSuccess = 0
	exitGeneric = 1
	exitInput   = 2
	exitConfig  = 3
	exitServer  = 4
	exitRuntime = 5
)

func main() {
	if err := newRootCmd().Execute(); err != nil {
		exitErr(err)
	}
}

func newRootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:           "rascal",
		Short:         "Rascal CLI for orchestrating autonomous coding runs",
		Long:          "Rascal is a CLI for starting, inspecting, and iterating autonomous coding runs on rascald.",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.initConfig()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.PersistentFlags().StringVar(&a.configPath, "config", config.DefaultClientConfigPath(), "config file path")
	root.PersistentFlags().StringVar(&a.serverURLFlag, "server-url", "", "orchestrator base URL")
	root.PersistentFlags().StringVar(&a.apiTokenFlag, "api-token", "", "orchestrator API token")
	root.PersistentFlags().StringVar(&a.defaultRepoFlag, "default-repo", "", "default repository in OWNER/REPO form")
	root.PersistentFlags().StringVar(&a.output, "output", "table", "output format: table|json|yaml")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "disable ANSI color/style output (also set by NO_COLOR)")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "reduce non-essential output")

	root.AddCommand(a.newInitCmd())
	root.AddCommand(a.newBootstrapCmd())
	root.AddCommand(a.newRunCmd())
	root.AddCommand(a.newIssueCmd())
	root.AddCommand(a.newPSCmd())
	root.AddCommand(a.newLogsCmd())
	root.AddCommand(a.newDoctorCmd())
	root.AddCommand(a.newOpenCmd())
	root.AddCommand(a.newRetryCmd())
	root.AddCommand(a.newCancelCmd())
	root.AddCommand(a.newTaskCmd())
	root.AddCommand(a.newConfigCmd())
	root.AddCommand(a.newAuthCmd())
	root.AddCommand(a.newRepoCmd())
	root.AddCommand(a.newInfraCmd())
	root.AddCommand(newCompletionCmd(root))

	return root
}

func (a *app) initConfig() error {
	v := viper.New()
	v.SetConfigFile(a.configPath)
	v.SetConfigType("yaml")
	v.SetDefault("server_url", "http://127.0.0.1:8080")
	v.SetEnvPrefix("RASCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return &cliError{
				Code:    exitConfig,
				Message: "failed to read config",
				Hint:    fmt.Sprintf("fix %s or run `rascal init`", a.configPath),
				Cause:   err,
			}
		}
	}
	a.cfg = config.ClientConfig{
		ServerURL:   strings.TrimSpace(v.GetString("server_url")),
		APIToken:    strings.TrimSpace(v.GetString("api_token")),
		DefaultRepo: strings.TrimSpace(v.GetString("default_repo")),
	}

	if strings.TrimSpace(a.serverURLFlag) != "" {
		a.cfg.ServerURL = strings.TrimSpace(a.serverURLFlag)
		a.serverSource = "flag"
	} else if strings.TrimSpace(os.Getenv("RASCAL_SERVER_URL")) != "" {
		a.serverSource = "env"
	} else if v.InConfig("server_url") {
		a.serverSource = "config"
	} else {
		a.serverSource = "default"
	}
	if strings.TrimSpace(a.apiTokenFlag) != "" {
		a.cfg.APIToken = strings.TrimSpace(a.apiTokenFlag)
		a.tokenSource = "flag"
	} else if strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")) != "" {
		a.tokenSource = "env"
	} else if v.InConfig("api_token") {
		a.tokenSource = "config"
	} else {
		a.tokenSource = "unset"
	}
	if strings.TrimSpace(a.defaultRepoFlag) != "" {
		a.cfg.DefaultRepo = strings.TrimSpace(a.defaultRepoFlag)
		a.repoSource = "flag"
	} else if strings.TrimSpace(os.Getenv("RASCAL_DEFAULT_REPO")) != "" {
		a.repoSource = "env"
	} else if v.InConfig("default_repo") {
		a.repoSource = "config"
	} else {
		a.repoSource = "unset"
	}

	a.cfg.ServerURL = strings.TrimRight(a.cfg.ServerURL, "/")
	if a.cfg.ServerURL == "" {
		a.cfg.ServerURL = "http://127.0.0.1:8080"
	}

	a.client = apiClient{
		baseURL: a.cfg.ServerURL,
		token:   a.cfg.APIToken,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	switch strings.ToLower(strings.TrimSpace(a.output)) {
	case "", "table":
		a.output = "table"
	case "json", "yaml":
	default:
		return &cliError{
			Code:    exitInput,
			Message: fmt.Sprintf("unsupported --output value %q", a.output),
			Hint:    "use --output table|json|yaml",
		}
	}

	return nil
}

func (a *app) isTTY() bool {
	st, err := os.Stdout.Stat()
	if err != nil {
		return false
	}
	return (st.Mode() & os.ModeCharDevice) != 0
}

func (a *app) ansiEnabled() bool {
	if noColorRequested(a.noColor) {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	return a.isTTY()
}

func (a *app) println(msg string, args ...any) {
	if a.quiet {
		return
	}
	fmt.Printf(msg+"\n", args...)
}

func (a *app) emit(v any, tableFn func() error) error {
	switch a.output {
	case "table":
		if tableFn == nil {
			return nil
		}
		return tableFn()
	case "json":
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		return enc.Encode(v)
	case "yaml":
		data, err := yaml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return &cliError{Code: exitInput, Message: "invalid output format", Hint: "use table|json|yaml"}
	}
}

func (a *app) requireServerAuth() error {
	if strings.TrimSpace(a.cfg.APIToken) != "" {
		return nil
	}
	return &cliError{
		Code:    exitConfig,
		Message: "missing API token",
		Hint:    "set RASCAL_API_TOKEN, configure ~/.rascal/config.yaml, or run `rascal init`",
	}
}

func (a *app) newInitCmd() *cobra.Command {
	var (
		serverURL      string
		apiToken       string
		defaultRepo    string
		nonInteractive bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize local Rascal CLI config",
		Long:  "Create or update local Rascal config at ~/.rascal/config.yaml (or --config path).",
		RunE: func(_ *cobra.Command, _ []string) error {
			serverURL = firstNonEmpty(strings.TrimSpace(serverURL), a.cfg.ServerURL)
			apiToken = firstNonEmpty(strings.TrimSpace(apiToken), a.cfg.APIToken)
			defaultRepo = firstNonEmpty(strings.TrimSpace(defaultRepo), a.cfg.DefaultRepo)

			if !nonInteractive && a.isTTY() {
				reader := bufio.NewReader(os.Stdin)
				serverURL = promptString(reader, "Server URL", serverURL)
				apiToken = promptString(reader, "API Token", apiToken)
				defaultRepo = promptString(reader, "Default Repo (optional)", defaultRepo)
			}

			if strings.TrimSpace(serverURL) == "" {
				return &cliError{Code: exitInput, Message: "server URL is required", Hint: "pass --server-url or run interactively"}
			}

			cfg := config.ClientConfig{
				ServerURL:   strings.TrimRight(serverURL, "/"),
				APIToken:    strings.TrimSpace(apiToken),
				DefaultRepo: strings.TrimSpace(defaultRepo),
			}
			if err := config.SaveClientConfig(a.configPath, cfg); err != nil {
				return &cliError{Code: exitConfig, Message: "failed to write config", Hint: "check file permissions", Cause: err}
			}
			a.println("config written: %s", a.configPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server-url", "", "orchestrator base URL")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "orchestrator API token")
	cmd.Flags().StringVar(&defaultRepo, "default-repo", "", "default repository OWNER/REPO")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts and rely on flags")
	return cmd
}

func (a *app) newBootstrapCmd() *cobra.Command {
	var (
		repo               string
		domain             string
		serverURL          string
		apiToken           string
		githubToken        string
		webhookSecret      string
		skipWebhook        bool
		writeConfig        bool
		host               string
		sshUser            string
		sshKey             string
		sshPort            int
		goarch             string
		deployExisting     bool
		codexAuthPath      string
		hcloudToken        string
		hcloudServerName   string
		hcloudServerType   string
		hcloudLocation     string
		hcloudImage        string
		hcloudSSHKeyName   string
		hcloudSSHPublicKey string
		hcloudFirewallName string
		hcloudApplyFW      bool
		hcloudTimeout      time.Duration
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Configure client and optionally deploy to an existing server",
		RunE: func(_ *cobra.Command, _ []string) error {
			repo = firstNonEmpty(strings.TrimSpace(repo), a.cfg.DefaultRepo)
			domain = strings.TrimSpace(domain)
			serverURL = strings.TrimSpace(serverURL)
			apiToken = strings.TrimSpace(apiToken)
			githubToken = strings.TrimSpace(githubToken)
			webhookSecret = strings.TrimSpace(webhookSecret)
			host = strings.TrimSpace(host)
			sshUser = strings.TrimSpace(sshUser)
			sshKey = strings.TrimSpace(sshKey)
			goarch = strings.TrimSpace(goarch)
			codexAuthPath = strings.TrimSpace(codexAuthPath)
			hcloudToken = firstNonEmpty(strings.TrimSpace(hcloudToken), strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")))
			hcloudServerName = strings.TrimSpace(hcloudServerName)
			hcloudServerType = strings.TrimSpace(hcloudServerType)
			hcloudLocation = strings.TrimSpace(hcloudLocation)
			hcloudImage = strings.TrimSpace(hcloudImage)
			hcloudSSHKeyName = strings.TrimSpace(hcloudSSHKeyName)
			hcloudSSHPublicKey = strings.TrimSpace(hcloudSSHPublicKey)
			hcloudFirewallName = strings.TrimSpace(hcloudFirewallName)

			var provisionOut *hcloudProvisionResult
			if hcloudToken != "" && host == "" {
				if hcloudTimeout <= 0 {
					hcloudTimeout = 8 * time.Minute
				}
				publicKeyPath, err := expandPath(firstNonEmpty(hcloudSSHPublicKey, "~/.ssh/id_ed25519.pub"))
				if err != nil {
					return fmt.Errorf("expand --hcloud-ssh-public-key path: %w", err)
				}
				if hcloudServerName == "" {
					hcloudServerName = fmt.Sprintf("rascal-%d", time.Now().UTC().Unix())
				}
				ctx, cancel := context.WithTimeout(context.Background(), hcloudTimeout)
				defer cancel()
				out, err := provisionHetznerServer(ctx, hcloudProvisionConfig{
					Token:         hcloudToken,
					ServerName:    hcloudServerName,
					ServerType:    firstNonEmpty(hcloudServerType, "cax11"),
					Location:      firstNonEmpty(hcloudLocation, "fsn1"),
					Image:         firstNonEmpty(hcloudImage, "ubuntu-24.04"),
					SSHKeyName:    firstNonEmpty(hcloudSSHKeyName, "rascal"),
					SSHPublicPath: publicKeyPath,
					FirewallName:  firstNonEmpty(hcloudFirewallName, "rascal-fw"),
					ApplyFirewall: hcloudApplyFW,
				})
				if err != nil {
					return fmt.Errorf("hcloud provision: %w", err)
				}
				host = strings.TrimSpace(out.Host)
				deployExisting = true
				provisionOut = &out
			}

			if serverURL == "" {
				switch {
				case domain != "":
					serverURL = "https://" + domain
				case host != "":
					serverURL = "http://" + host + ":8080"
				default:
					return fmt.Errorf("either --server-url, --domain, or a provisioned/explicit --host is required")
				}
			}
			serverURL = strings.TrimRight(serverURL, "/")

			if apiToken == "" {
				tok, err := randomToken(32)
				if err != nil {
					return fmt.Errorf("generate api token: %w", err)
				}
				apiToken = tok
			}
			if webhookSecret == "" {
				secret, err := randomToken(32)
				if err != nil {
					return fmt.Errorf("generate webhook secret: %w", err)
				}
				webhookSecret = secret
			}

			if writeConfig {
				if err := config.SaveClientConfig(a.configPath, config.ClientConfig{
					ServerURL:   serverURL,
					APIToken:    apiToken,
					DefaultRepo: repo,
				}); err != nil {
					return err
				}
			}

			if !skipWebhook {
				if repo == "" {
					return fmt.Errorf("--repo is required when webhook setup is enabled")
				}
				if githubToken == "" {
					return fmt.Errorf("--github-token is required when webhook setup is enabled")
				}
				gh := ghapi.NewAPIClient(githubToken)
				ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
				defer cancel()

				if err := gh.EnsureLabel(ctx, repo, "rascal", "0e8a16", "Trigger Rascal automation"); err != nil {
					return fmt.Errorf("ensure label: %w", err)
				}
				if err := gh.UpsertWebhook(ctx, repo, serverURL+"/v1/webhooks/github", webhookSecret, nil); err != nil {
					return fmt.Errorf("upsert webhook: %w", err)
				}
			}

			if deployExisting {
				if host == "" {
					return fmt.Errorf("--host is required when --deploy-existing is set")
				}
				if githubToken == "" {
					return fmt.Errorf("--github-token is required when --deploy-existing is set")
				}
				if sshPort <= 0 {
					return fmt.Errorf("--ssh-port must be positive")
				}
				if codexAuthPath == "" {
					return fmt.Errorf("--codex-auth must be set")
				}
				expandedAuthPath, err := expandPath(codexAuthPath)
				if err != nil {
					return fmt.Errorf("expand codex auth path: %w", err)
				}
					deployCfg := deployConfig{
						Host:               host,
						SSHUser:            sshUser,
						SSHKeyPath:         sshKey,
						SSHPort:            sshPort,
						Domain:             domain,
						APIToken:           apiToken,
						WebhookSecret:      webhookSecret,
						GitHubToken:        githubToken,
					CodexAuthPath:      expandedAuthPath,
					RunnerMode:         "docker",
					RunnerImage:        "rascal-runner:latest",
					ServerListenAddr:   ":8080",
					ServerDataDir:      "/var/lib/rascal",
					ServerStatePath:    "/var/lib/rascal/state.json",
					ServerCodexAuthDst: "/etc/rascal/codex_auth.json",
					GOARCH:             goarch,
				}
				if err := deployToExistingHost(deployCfg); err != nil {
					return err
				}
				a.println("deployed rascald to %s", host)
			}
			out := map[string]any{
				"status":         "bootstrap_complete",
				"server_url":     serverURL,
				"api_token":      maskSecret(apiToken),
				"default_repo":   repo,
				"webhook_secret": maskSecret(webhookSecret),
			}
			if writeConfig {
				out["config_path"] = a.configPath
			}
				if provisionOut != nil {
					out["provisioned_server"] = provisionOut
					out["host"] = host
				}
				return a.emit(out, func() error {
					a.println("bootstrap complete")
				a.println("server_url: %s", serverURL)
				a.println("api_token: %s", maskSecret(apiToken))
				a.println("default_repo: %s", repo)
				a.println("webhook_secret: %s", maskSecret(webhookSecret))
				if writeConfig {
					a.println("config_path: %s", a.configPath)
				}
					if provisionOut != nil {
						a.println("provisioned host: %s", host)
					}
					return nil
				})
		},
	}

	cmd.Flags().StringVar(&repo, "repo", "", "default repository in OWNER/REPO form")
	cmd.Flags().StringVar(&domain, "domain", "", "orchestrator domain (e.g. rascal.example.com)")
	cmd.Flags().StringVar(&serverURL, "server-url", "", "orchestrator base URL (overrides --domain)")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "API token for orchestrator (auto-generated if empty)")
	cmd.Flags().StringVar(&githubToken, "github-token", "", "GitHub token for label/webhook setup")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret (auto-generated if empty)")
	cmd.Flags().BoolVar(&skipWebhook, "skip-webhook", false, "skip GitHub webhook setup")
	cmd.Flags().BoolVar(&writeConfig, "write-config", true, "write config file")
	cmd.Flags().StringVar(&host, "host", "", "existing server host")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH user for existing host deployment")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path for existing host deployment")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH port for existing host deployment")
	cmd.Flags().StringVar(&goarch, "goarch", "amd64", "GOARCH for rascald binary when deploying to existing host")
	cmd.Flags().BoolVar(&deployExisting, "deploy-existing", false, "deploy rascald to --host over SSH")
	cmd.Flags().StringVar(&codexAuthPath, "codex-auth", "~/.codex/auth.json", "local Codex auth.json path copied to the server")
	cmd.Flags().StringVar(&hcloudToken, "hcloud-token", "", "Hetzner Cloud token (provisions host when set and --host is empty)")
	cmd.Flags().StringVar(&hcloudServerName, "hcloud-server-name", "", "Hetzner server name")
	cmd.Flags().StringVar(&hcloudServerType, "hcloud-server-type", "cax11", "Hetzner server type")
	cmd.Flags().StringVar(&hcloudLocation, "hcloud-location", "fsn1", "Hetzner location")
	cmd.Flags().StringVar(&hcloudImage, "hcloud-image", "ubuntu-24.04", "Hetzner image")
	cmd.Flags().StringVar(&hcloudSSHKeyName, "hcloud-ssh-key-name", "rascal", "Hetzner SSH key resource name")
	cmd.Flags().StringVar(&hcloudSSHPublicKey, "hcloud-ssh-public-key", "~/.ssh/id_ed25519.pub", "local SSH public key path for Hetzner")
	cmd.Flags().StringVar(&hcloudFirewallName, "hcloud-firewall-name", "rascal-fw", "Hetzner firewall resource name")
	cmd.Flags().BoolVar(&hcloudApplyFW, "hcloud-apply-firewall", true, "create/update and attach firewall (22,80,443)")
	cmd.Flags().DurationVar(&hcloudTimeout, "hcloud-timeout", 8*time.Minute, "Hetzner provisioning timeout")

	return cmd
}

func (a *app) newRunCmd() *cobra.Command {
	var repo, task, baseBranch string
	cmd := &cobra.Command{
		Use:     "run",
		Short:   "Start an ad-hoc run",
		Example: "  rascal run -R owner/repo -t \"fix flaky tests\"\n  rascal run --repo owner/repo --task \"refactor\" --output json",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo = firstNonEmpty(strings.TrimSpace(repo), a.cfg.DefaultRepo)
			task = strings.TrimSpace(task)
			baseBranch = firstNonEmpty(strings.TrimSpace(baseBranch), "main")
			if repo == "" || task == "" {
				return &cliError{Code: exitInput, Message: "both --repo/-R and --task/-t are required"}
			}

			payload := map[string]any{"repo": repo, "task": task, "base_branch": baseBranch}
			resp, err := a.client.doJSON(http.MethodPost, "/v1/tasks", payload)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Hint: "verify server URL and network access", Cause: err}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out struct {
				Run state.Run `json:"run"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(map[string]any{"run": out.Run}, func() error {
				a.println("run created: %s (%s)", out.Run.ID, out.Run.Status)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "R", "", "repository in OWNER/REPO form")
	cmd.Flags().StringVarP(&task, "task", "t", "", "task text")
	cmd.Flags().StringVarP(&baseBranch, "base-branch", "b", "main", "base branch")
	return cmd
}

func (a *app) newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:     "issue OWNER/REPO#123",
		Short:   "Start a run from an issue",
		Example: "  rascal issue owner/repo#123\n  rascal issue owner/repo#123 --output json",
		Args:    cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			repo, issueNumber, err := parseIssueRef(args[0])
			if err != nil {
				return &cliError{Code: exitInput, Message: err.Error()}
			}
			payload := map[string]any{"repo": repo, "issue_number": issueNumber}
			resp, err := a.client.doJSON(http.MethodPost, "/v1/tasks/issue", payload)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out struct {
				Run state.Run `json:"run"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(map[string]any{"run": out.Run}, func() error {
				a.println("issue run created: %s (%s)", out.Run.ID, out.Run.Status)
				return nil
			})
		},
	}
	return cmd
}

func (a *app) newPSCmd() *cobra.Command {
	var (
		limit    int
		watch    bool
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:     "ps",
		Aliases: []string{"ls"},
		Short:   "List recent runs",
		Example: "  rascal ps\n  rascal ps --watch\n  rascal ps --output json",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			if watch && a.output != "table" {
				return &cliError{Code: exitInput, Message: "--watch is only supported with --output table"}
			}

			render := func(runs []state.Run) error {
				return a.emit(map[string]any{"runs": runs}, func() error {
					tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
					fmt.Fprintln(tw, "RUN ID\tSTATUS\tREPO\tTASK ID\tCREATED")
					for _, run := range runs {
						fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", run.ID, run.Status, run.Repo, run.TaskID, run.CreatedAt.Format(time.RFC3339))
					}
					return tw.Flush()
				})
			}

			if !watch {
				runs, err := a.fetchRuns(limit)
				if err != nil {
					return err
				}
				return render(runs)
			}

			if interval <= 0 {
				interval = 2 * time.Second
			}
			sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()

			for {
				runs, err := a.fetchRuns(limit)
				if err != nil {
					return err
				}
				if a.ansiEnabled() {
					_, _ = fmt.Fprint(os.Stdout, "\033[H\033[2J")
				}
				_ = render(runs)
				select {
				case <-sigCtx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	cmd.Flags().IntVar(&limit, "limit", 50, "max number of runs")
	cmd.Flags().BoolVar(&watch, "watch", false, "refresh continuously")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "refresh interval when --watch is enabled")
	return cmd
}

func (a *app) newLogsCmd() *cobra.Command {
	var (
		follow   bool
		interval time.Duration
		since    time.Duration
	)
	cmd := &cobra.Command{
		Use:               "logs <run_id>",
		Aliases:           []string{"tail"},
		Short:             "Fetch logs for a run",
		Example:           "  rascal logs run_abc123\n  rascal logs run_abc123 --follow --interval 2s",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			if interval <= 0 {
				interval = 2 * time.Second
			}

			fetch := func() (string, error) {
				resp, err := a.client.do(http.MethodGet, "/v1/runs/"+args[0]+"/logs", nil)
				if err != nil {
					return "", &cliError{Code: exitServer, Message: "request failed", Cause: err}
				}
				defer resp.Body.Close()
				if resp.StatusCode >= 300 {
					return "", decodeServerError(resp)
				}
				body, err := io.ReadAll(resp.Body)
				if err != nil {
					return "", err
				}
				return string(body), nil
			}

			if !follow {
				body, err := fetch()
				if err != nil {
					return err
				}
				if since > 0 {
					body = filterLogsSince(body, time.Now().Add(-since))
				}
				_, err = io.WriteString(os.Stdout, body)
				return err
			}

			sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
			defer stop()
			last := ""
			for {
				body, err := fetch()
				if err != nil {
					return err
				}
				if since > 0 {
					body = filterLogsSince(body, time.Now().Add(-since))
				}
				if strings.HasPrefix(body, last) {
					diff := strings.TrimPrefix(body, last)
					if diff != "" {
						_, _ = io.WriteString(os.Stdout, diff)
					}
				} else if body != last {
					_, _ = io.WriteString(os.Stdout, body)
				}
				last = body
				select {
				case <-sigCtx.Done():
					return nil
				case <-time.After(interval):
				}
			}
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "stream logs by polling")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "polling interval when --follow is enabled")
	cmd.Flags().DurationVar(&since, "since", 0, "show logs since duration ago (best effort)")
	return cmd
}

func (a *app) newDoctorCmd() *cobra.Command {
	var fix bool
	cmd := &cobra.Command{
		Use:     "doctor",
		Short:   "Inspect local CLI configuration",
		Example: "  rascal doctor\n  rascal doctor --fix",
		RunE: func(_ *cobra.Command, _ []string) error {
			_, statErr := os.Stat(a.configPath)
			cfgExists := statErr == nil

			if fix && !cfgExists {
				if err := config.SaveClientConfig(a.configPath, a.cfg); err != nil {
					return &cliError{Code: exitConfig, Message: "failed to auto-fix config", Cause: err}
				}
				cfgExists = true
			}

			diagnostics := map[string]any{
				"config_path":         a.configPath,
				"config_exists":       cfgExists,
				"server_url":          a.cfg.ServerURL,
				"server_source":       a.serverSource,
				"api_token_set":       strings.TrimSpace(a.cfg.APIToken) != "",
				"api_token_source":    a.tokenSource,
				"default_repo":        a.cfg.DefaultRepo,
				"default_repo_source": a.repoSource,
				"output_format":       a.output,
				"no_color":            noColorRequested(a.noColor),
			}
			return a.emit(diagnostics, func() error {
				a.println("config path: %s", a.configPath)
				if cfgExists {
					a.println("config file: present")
				} else {
					a.println("config file: missing")
				}
				a.println("server: %s (%s)", a.cfg.ServerURL, a.serverSource)
				if a.cfg.APIToken == "" {
					a.println("api token: missing")
				} else {
					a.println("api token: set (%s)", a.tokenSource)
				}
				if a.cfg.DefaultRepo == "" {
					a.println("default repo: not set")
				} else {
					a.println("default repo: %s (%s)", a.cfg.DefaultRepo, a.repoSource)
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "attempt safe auto-fixes (create config file)")
	return cmd
}

func (a *app) newOpenCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "open <run_id>",
		Short:             "Print PR URL for a run",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			run, err := a.fetchRun(args[0])
			if err != nil {
				return err
			}
			if strings.TrimSpace(run.PRURL) == "" {
				return &cliError{Code: exitRuntime, Message: "run has no PR URL yet", Hint: "wait for run completion and PR creation"}
			}
			a.println(run.PRURL)
			return nil
		},
	}
	return cmd
}

func (a *app) newRetryCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "retry <run_id>",
		Aliases:           []string{"rerun"},
		Short:             "Create a new run from an existing run",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			run, err := a.fetchRun(args[0])
			if err != nil {
				return err
			}
			if run.Status != state.StatusFailed && run.Status != state.StatusCanceled {
				return &cliError{Code: exitInput, Message: "retry only supports failed or canceled runs"}
			}
			payload := map[string]any{
				"task_id":     run.TaskID,
				"repo":        run.Repo,
				"task":        run.Task,
				"base_branch": run.BaseBranch,
			}
			resp, err := a.client.doJSON(http.MethodPost, "/v1/tasks", payload)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out struct {
				Run state.Run `json:"run"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(map[string]any{"run": out.Run}, func() error {
				a.println("retry run created: %s (%s)", out.Run.ID, out.Run.Status)
				return nil
			})
		},
	}
	return cmd
}

func (a *app) newCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:               "cancel <run_id>",
		Short:             "Cancel a queued or running run",
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			resp, err := a.client.do(http.MethodPost, "/v1/runs/"+args[0]+"/cancel", nil)
			if err != nil {
				return &cliError{Code: exitServer, Message: "request failed", Cause: err}
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				return decodeServerError(resp)
			}
			var out map[string]any
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
			}
			return a.emit(out, func() error {
				a.println("cancel request submitted for %s", args[0])
				return nil
			})
		},
	}
	return cmd
}

func (a *app) newTaskCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "task <task_id>",
		Short: "Show task status/details",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			task, err := a.fetchTask(args[0])
			if err != nil {
				return err
			}
			return a.emit(map[string]any{"task": task}, func() error {
				tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
				fmt.Fprintln(tw, "TASK ID\tSTATUS\tREPO\tPR\tPENDING INPUT\tUPDATED")
				fmt.Fprintf(tw, "%s\t%s\t%s\t%d\t%t\t%s\n", task.ID, task.Status, task.Repo, task.PRNumber, task.PendingInput, task.UpdatedAt.Format(time.RFC3339))
				return tw.Flush()
			})
		},
	}
	return cmd
}

func (a *app) newConfigCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect effective config and manage local file values",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "view",
		Short: "View effective config (flags/env/file)",
		RunE: func(_ *cobra.Command, _ []string) error {
			view := map[string]any{
				"config_path":         a.configPath,
				"server_url":          a.cfg.ServerURL,
				"api_token":           maskSecret(a.cfg.APIToken),
				"default_repo":        a.cfg.DefaultRepo,
				"server_source":       a.serverSource,
				"api_token_source":    a.tokenSource,
				"default_repo_source": a.repoSource,
			}
			return a.emit(view, func() error {
				for _, key := range []string{"config_path", "server_url", "api_token", "default_repo", "server_source", "api_token_source", "default_repo_source"} {
					a.println("%s: %v", key, view[key])
				}
				return nil
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a config key from the local config file",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadFileConfig(a.configPath)
			if err != nil {
				return err
			}
			key := strings.TrimSpace(args[0])
			switch key {
			case "server_url":
				a.println(cfg.ServerURL)
			case "api_token":
				a.println(maskSecret(cfg.APIToken))
			case "default_repo":
				a.println(cfg.DefaultRepo)
			default:
				return &cliError{Code: exitInput, Message: "invalid key", Hint: "use server_url|api_token|default_repo"}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config key in the local config file",
		Args:  cobra.ExactArgs(2),
		RunE: func(_ *cobra.Command, args []string) error {
			cfg, err := loadFileConfig(a.configPath)
			if err != nil {
				return err
			}
			key, val := strings.TrimSpace(args[0]), strings.TrimSpace(args[1])
			switch key {
			case "server_url":
				cfg.ServerURL = strings.TrimRight(val, "/")
			case "api_token":
				cfg.APIToken = val
			case "default_repo":
				cfg.DefaultRepo = val
			default:
				return &cliError{Code: exitInput, Message: "invalid key", Hint: "use server_url|api_token|default_repo"}
			}
			if err := config.SaveClientConfig(a.configPath, cfg); err != nil {
				return &cliError{Code: exitConfig, Message: "failed to write config", Cause: err}
			}
			a.println("updated %s in %s", key, a.configPath)
			return nil
		},
	})
	return cmd
}

func (a *app) newAuthCmd() *cobra.Command {
	var (
		writeConfig bool
		showRaw     bool
	)
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication helpers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Generate fresh API and webhook tokens",
		RunE: func(_ *cobra.Command, _ []string) error {
			apiToken, err := randomToken(32)
			if err != nil {
				return err
			}
			webhookSecret, err := randomToken(32)
			if err != nil {
				return err
			}
			if writeConfig {
				cfg, err := loadFileConfig(a.configPath)
				if err != nil {
					return err
				}
				cfg.APIToken = apiToken
				if err := config.SaveClientConfig(a.configPath, cfg); err != nil {
					return &cliError{Code: exitConfig, Message: "failed to write config", Cause: err}
				}
			}
			displayAPI := maskSecret(apiToken)
			displayWebhook := maskSecret(webhookSecret)
			if showRaw {
				displayAPI = apiToken
				displayWebhook = webhookSecret
			}
			out := map[string]any{
				"api_token":      displayAPI,
				"webhook_secret": displayWebhook,
				"write_config":   writeConfig,
			}
			return a.emit(out, func() error {
				a.println("api_token: %s", displayAPI)
				a.println("webhook_secret: %s", displayWebhook)
				if !showRaw {
					a.println("use --show to print raw values")
				}
				return nil
			})
		},
	})
	cmd.PersistentFlags().BoolVar(&writeConfig, "write-config", false, "write generated API token to local config")
	cmd.PersistentFlags().BoolVar(&showRaw, "show", false, "print raw token values")
	return cmd
}

func newCompletionCmd(root *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:       "completion [bash|zsh|fish|powershell]",
		Short:     "Generate shell completion scripts",
		Long:      "Generate shell completion scripts for Bash, Zsh, Fish, or PowerShell.",
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			switch args[0] {
			case "bash":
				return root.GenBashCompletionV2(cmd.OutOrStdout(), true)
			case "zsh":
				return root.GenZshCompletion(cmd.OutOrStdout())
			case "fish":
				return root.GenFishCompletion(cmd.OutOrStdout(), true)
			case "powershell":
				return root.GenPowerShellCompletionWithDesc(cmd.OutOrStdout())
			default:
				return fmt.Errorf("unsupported shell %q", args[0])
			}
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:       "install [bash|zsh|fish|powershell]",
		Short:     "Install completion script to a standard user path",
		Args:      cobra.ExactValidArgs(1),
		ValidArgs: []string{"bash", "zsh", "fish", "powershell"},
		RunE: func(cmd *cobra.Command, args []string) error {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			var (
				target string
				data   bytes.Buffer
			)
			switch args[0] {
			case "bash":
				target = filepath.Join(home, ".local", "share", "bash-completion", "completions", "rascal")
				if err := root.GenBashCompletionV2(&data, true); err != nil {
					return err
				}
			case "zsh":
				target = filepath.Join(home, ".zfunc", "_rascal")
				if err := root.GenZshCompletion(&data); err != nil {
					return err
				}
			case "fish":
				target = filepath.Join(home, ".config", "fish", "completions", "rascal.fish")
				if err := root.GenFishCompletion(&data, true); err != nil {
					return err
				}
			case "powershell":
				target = filepath.Join(home, "Documents", "PowerShell", "Modules", "rascal_completion.ps1")
				if err := root.GenPowerShellCompletionWithDesc(&data); err != nil {
					return err
				}
			}
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(target, data.Bytes(), 0o644); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(cmd.OutOrStdout(), "installed completion: %s\n", target)
			return nil
		},
	})
	return cmd
}

func (a *app) runIDCompletion(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
	if len(args) > 0 {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	runs, err := a.fetchRuns(100)
	if err != nil {
		return nil, cobra.ShellCompDirectiveNoFileComp
	}
	out := make([]string, 0, len(runs))
	for _, run := range runs {
		if strings.HasPrefix(run.ID, toComplete) {
			out = append(out, run.ID)
		}
	}
	sort.Strings(out)
	return out, cobra.ShellCompDirectiveNoFileComp
}

func (a *app) fetchRuns(limit int) ([]state.Run, error) {
	if limit <= 0 {
		limit = 50
	}
	resp, err := a.client.do(http.MethodGet, fmt.Sprintf("/v1/runs?limit=%d", limit), nil)
	if err != nil {
		return nil, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return nil, decodeServerError(resp)
	}
	var out struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Runs, nil
}

func (a *app) fetchRun(runID string) (state.Run, error) {
	resp, err := a.client.do(http.MethodGet, "/v1/runs/"+runID, nil)
	if err != nil {
		return state.Run{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return state.Run{}, decodeServerError(resp)
	}
	var out struct {
		Run state.Run `json:"run"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return state.Run{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Run, nil
}

func (a *app) fetchTask(taskID string) (state.Task, error) {
	escaped := url.PathEscape(taskID)
	resp, err := a.client.do(http.MethodGet, "/v1/tasks/"+escaped, nil)
	if err != nil {
		return state.Task{}, &cliError{Code: exitServer, Message: "request failed", Cause: err}
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		return state.Task{}, decodeServerError(resp)
	}
	var out struct {
		Task state.Task `json:"task"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return state.Task{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
	}
	return out.Task, nil
}

func decodeServerError(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	msg := strings.TrimSpace(string(body))
	if msg == "" {
		msg = http.StatusText(resp.StatusCode)
	}
	reqID := strings.TrimSpace(resp.Header.Get("X-Request-ID"))
	hint := ""
	if reqID != "" {
		hint = "request id: " + reqID
	}
	return &cliError{
		Code:      exitServer,
		Message:   fmt.Sprintf("server error (%d): %s", resp.StatusCode, msg),
		Hint:      hint,
		RequestID: reqID,
	}
}

func loadFileConfig(path string) (config.ClientConfig, error) {
	v := viper.New()
	v.SetConfigFile(path)
	v.SetConfigType("yaml")
	v.SetDefault("server_url", "http://127.0.0.1:8080")
	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return config.ClientConfig{}, &cliError{Code: exitConfig, Message: "failed to read config file", Cause: err}
		}
	}
	return config.ClientConfig{
		ServerURL:   strings.TrimRight(strings.TrimSpace(v.GetString("server_url")), "/"),
		APIToken:    strings.TrimSpace(v.GetString("api_token")),
		DefaultRepo: strings.TrimSpace(v.GetString("default_repo")),
	}, nil
}

func noColorRequested(flagValue bool) bool {
	if flagValue {
		return true
	}
	_, set := os.LookupEnv("NO_COLOR")
	return set
}

func promptString(r *bufio.Reader, label, def string) string {
	if strings.TrimSpace(def) != "" {
		fmt.Printf("%s [%s]: ", label, def)
	} else {
		fmt.Printf("%s: ", label)
	}
	line, _ := r.ReadString('\n')
	line = strings.TrimSpace(line)
	if line == "" {
		return def
	}
	return line
}

func filterLogsSince(input string, since time.Time) string {
	if input == "" {
		return input
	}
	lines := strings.Split(input, "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if strings.TrimSpace(line) == "" {
			out = append(out, line)
			continue
		}
		if strings.HasPrefix(line, "[") {
			end := strings.Index(line, "]")
			if end > 1 {
				ts := strings.TrimSpace(line[1:end])
				if parsed, err := time.Parse(time.RFC3339, ts); err == nil {
					if parsed.Before(since) {
						continue
					}
				}
			}
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

func parseIssueRef(input string) (string, int, error) {
	parts := strings.Split(input, "#")
	if len(parts) != 2 {
		return "", 0, fmt.Errorf("invalid issue ref %q, expected OWNER/REPO#123", input)
	}
	repo := strings.TrimSpace(parts[0])
	var issue int
	if _, err := fmt.Sscanf(parts[1], "%d", &issue); err != nil || issue <= 0 {
		return "", 0, fmt.Errorf("invalid issue number in %q", input)
	}
	if repo == "" {
		return "", 0, fmt.Errorf("invalid repo in %q", input)
	}
	return repo, issue, nil
}

func (c apiClient) doJSON(method, path string, payload any) (*http.Response, error) {
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, fmt.Errorf("encode payload: %w", err)
	}
	return c.do(method, path, bytes.NewReader(data))
}

func (c apiClient) do(method, path string, body io.Reader) (*http.Response, error) {
	req, err := http.NewRequest(method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("request failed: %w", err)
	}
	return resp, nil
}

func randomToken(numBytes int) (string, error) {
	if numBytes <= 0 {
		numBytes = 32
	}
	buf := make([]byte, numBytes)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

type deployConfig struct {
	Host               string
	SSHUser            string
	SSHKeyPath         string
	SSHPort            int
	Domain             string
	APIToken           string
	WebhookSecret      string
	GitHubToken        string
	CodexAuthPath      string
	RunnerMode         string
	RunnerImage        string
	ServerListenAddr   string
	ServerDataDir      string
	ServerStatePath    string
	ServerCodexAuthDst string
	GOARCH             string
}

func deployToExistingHost(cfg deployConfig) error {
	if strings.TrimSpace(cfg.CodexAuthPath) == "" {
		return fmt.Errorf("codex auth path is required")
	}
	if _, err := os.Stat(cfg.CodexAuthPath); err != nil {
		return fmt.Errorf("codex auth file is required at %s: %w", cfg.CodexAuthPath, err)
	}

	tmpDir, err := os.MkdirTemp("", "rascal-bootstrap-*")
	if err != nil {
		return fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	binaryPath := filepath.Join(tmpDir, "rascald")
	if err := buildLinuxRascald(binaryPath, cfg.GOARCH); err != nil {
		return err
	}

	envPath := filepath.Join(tmpDir, "rascal.env")
	if err := os.WriteFile(envPath, []byte(serverEnvFile(cfg)), 0o600); err != nil {
		return fmt.Errorf("write env file: %w", err)
	}

	servicePath := filepath.Join(tmpDir, "rascal.service")
	if err := os.WriteFile(servicePath, []byte(systemdServiceContent()), 0o644); err != nil {
		return fmt.Errorf("write systemd service: %w", err)
	}

	runnerArchivePath := filepath.Join(tmpDir, "runner.tgz")
	if err := runLocal("tar", "-C", repoRootPath(), "-czf", runnerArchivePath, "runner"); err != nil {
		return fmt.Errorf("package runner assets: %w", err)
	}

	installDockerPath := deployAssetPath(filepath.Join("deploy", "scripts", "install_docker.sh"))
	if _, err := os.Stat(installDockerPath); err != nil {
		return fmt.Errorf("missing deploy script %s: %w", installDockerPath, err)
	}

	caddyPath := ""
	if strings.TrimSpace(cfg.Domain) != "" {
		caddyPath = filepath.Join(tmpDir, "Caddyfile")
		if err := os.WriteFile(caddyPath, []byte(renderCaddyfile(cfg.Domain)), 0o644); err != nil {
			return fmt.Errorf("write caddyfile: %w", err)
		}
	}

	if err := runLocal("ssh", sshArgs(cfg, "mkdir -p /opt/rascal /etc/rascal /var/lib/rascal /tmp/rascal-bootstrap /etc/caddy")...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, binaryPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/rascald"))...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, envPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/rascal.env"))...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, servicePath, remoteTarget(cfg, "/tmp/rascal-bootstrap/rascal.service"))...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, cfg.CodexAuthPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/auth.json"))...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, installDockerPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/install_docker.sh"))...); err != nil {
		return err
	}
	if err := runLocal("scp", scpArgs(cfg, runnerArchivePath, remoteTarget(cfg, "/tmp/rascal-bootstrap/runner.tgz"))...); err != nil {
		return err
	}
	if caddyPath != "" {
		if err := runLocal("scp", scpArgs(cfg, caddyPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/Caddyfile"))...); err != nil {
			return err
		}
	}

	remoteInstall := strings.Join([]string{
		"set -euo pipefail",
		"chmod +x /tmp/rascal-bootstrap/install_docker.sh",
		"/tmp/rascal-bootstrap/install_docker.sh",
		"if ! command -v caddy >/dev/null 2>&1; then apt-get update && DEBIAN_FRONTEND=noninteractive apt-get install -y caddy; fi",
		"mkdir -p /opt/rascal",
		"tar -xzf /tmp/rascal-bootstrap/runner.tgz -C /opt/rascal",
		fmt.Sprintf("docker build -t %s /opt/rascal/runner", cfg.RunnerImage),
		"install -m 0755 /tmp/rascal-bootstrap/rascald /opt/rascal/rascald",
		"install -m 0600 /tmp/rascal-bootstrap/rascal.env /etc/rascal/rascal.env",
		"install -m 0644 /tmp/rascal-bootstrap/rascal.service /etc/systemd/system/rascal.service",
		fmt.Sprintf("install -m 0600 /tmp/rascal-bootstrap/auth.json %s", cfg.ServerCodexAuthDst),
		"if [ -f /tmp/rascal-bootstrap/Caddyfile ]; then install -m 0644 /tmp/rascal-bootstrap/Caddyfile /etc/caddy/Caddyfile && systemctl enable caddy --now; fi",
		"systemctl daemon-reload",
		"systemctl enable rascal --now",
	}, " && ")
	if err := runLocal("ssh", sshArgs(cfg, remoteInstall)...); err != nil {
		return err
	}
	return nil
}

func buildLinuxRascald(outputPath, goarch string) error {
	if strings.TrimSpace(goarch) == "" {
		goarch = "amd64"
	}
	cmd := exec.Command("go", "build", "-o", outputPath, "./cmd/rascald")
	cmd.Env = append(os.Environ(), "GOOS=linux", "GOARCH="+goarch, "CGO_ENABLED=0")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("build rascald: %w", err)
	}
	return nil
}

func runLocal(name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s failed: %w", name, err)
	}
	return nil
}

func sshArgs(cfg deployConfig, remoteCmd string) []string {
	args := []string{"-p", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if cfg.SSHKeyPath != "" {
		args = append(args, "-i", cfg.SSHKeyPath)
	}
	args = append(args, fmt.Sprintf("%s@%s", cfg.SSHUser, cfg.Host), remoteCmd)
	return args
}

func scpArgs(cfg deployConfig, source, target string) []string {
	args := []string{"-P", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if cfg.SSHKeyPath != "" {
		args = append(args, "-i", cfg.SSHKeyPath)
	}
	args = append(args, source, target)
	return args
}

func remoteTarget(cfg deployConfig, path string) string {
	return fmt.Sprintf("%s@%s:%s", cfg.SSHUser, cfg.Host, path)
}

func serverEnvFile(cfg deployConfig) string {
	return fmt.Sprintf(strings.TrimSpace(`
RASCAL_LISTEN_ADDR=%s
RASCAL_DATA_DIR=%s
RASCAL_STATE_PATH=%s
RASCAL_API_TOKEN=%s
RASCAL_GITHUB_TOKEN=%s
RASCAL_GITHUB_WEBHOOK_SECRET=%s
RASCAL_RUNNER_MODE=%s
RASCAL_RUNNER_IMAGE=%s
RASCAL_RUNNER_MAX_ATTEMPTS=1
RASCAL_CODEX_AUTH_PATH=%s
	`)+"\n",
		cfg.ServerListenAddr,
		cfg.ServerDataDir,
		cfg.ServerStatePath,
		cfg.APIToken,
		cfg.GitHubToken,
		cfg.WebhookSecret,
		cfg.RunnerMode,
		cfg.RunnerImage,
		cfg.ServerCodexAuthDst,
	)
}

func systemdServiceContent() string {
	return strings.TrimSpace(`
[Unit]
Description=Rascal orchestrator service
After=network-online.target docker.service
Wants=network-online.target

[Service]
Type=simple
EnvironmentFile=/etc/rascal/rascal.env
ExecStart=/opt/rascal/rascald
Restart=always
RestartSec=3
User=root
WorkingDirectory=/opt/rascal

[Install]
WantedBy=multi-user.target
`) + "\n"
}

func renderCaddyfile(domain string) string {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return ""
	}
	templatePath := deployAssetPath(filepath.Join("deploy", "caddy", "Caddyfile.tmpl"))
	data, err := os.ReadFile(templatePath)
	if err == nil {
		return strings.ReplaceAll(string(data), "{{DOMAIN}}", domain)
	}
	return strings.TrimSpace(fmt.Sprintf(`
%s {
  encode gzip zstd
  reverse_proxy 127.0.0.1:8080
  log {
    output file /var/log/caddy/rascal-access.log
    format json
  }
}
`, domain)) + "\n"
}

func expandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func maskSecret(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	if len(value) <= 8 {
		return strings.Repeat("*", len(value))
	}
	return value[:4] + strings.Repeat("*", len(value)-8) + value[len(value)-4:]
}

func exitErr(err error) {
	code := exitGeneric
	var ce *cliError
	if errors.As(err, &ce) {
		if ce.Code != 0 {
			code = ce.Code
		}
		fmt.Fprintln(os.Stderr, "error:", ce.Error())
		if strings.TrimSpace(ce.Hint) != "" {
			fmt.Fprintln(os.Stderr, "hint:", ce.Hint)
		}
		if ce.Cause != nil {
			fmt.Fprintln(os.Stderr, "cause:", ce.Cause)
		}
		if ce.RequestID != "" {
			fmt.Fprintln(os.Stderr, "request_id:", ce.RequestID)
		}
		os.Exit(code)
	}
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(code)
}
