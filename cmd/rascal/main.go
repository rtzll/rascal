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
	"runtime"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/rtzll/rascal/internal/config"
	deployengine "github.com/rtzll/rascal/internal/deploy"
	"github.com/rtzll/rascal/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

type apiClient struct {
	baseURL   string
	token     string
	http      *http.Client
	transport string
	sshHost   string
	sshUser   string
	sshKey    string
	sshPort   int
}

type app struct {
	configPath      string
	envFilePath     string
	serverURLFlag   string
	apiTokenFlag    string
	defaultRepoFlag string
	transportFlag   string
	sshHostFlag     string
	sshUserFlag     string
	sshKeyFlag      string
	sshPortFlag     int
	output          string
	noColor         bool
	quiet           bool
	cfg             config.ClientConfig
	client          apiClient
	serverSource    string
	tokenSource     string
	repoSource      string
	transportSource string
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
			if err := a.loadGlobalEnv(); err != nil {
				return &cliError{Code: exitConfig, Message: "failed to load env file", Cause: err}
			}
			return a.initConfig()
		},
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}

	root.AddGroup(
		&cobra.Group{ID: "setup", Title: "Setup:"},
		&cobra.Group{ID: "runs", Title: "Runs:"},
		&cobra.Group{ID: "logs", Title: "Logs:"},
		&cobra.Group{ID: "integrations", Title: "Integrations:"},
		&cobra.Group{ID: "utilities", Title: "Utilities:"},
	)

	root.PersistentFlags().StringVar(&a.configPath, "config", config.DefaultClientConfigPath(), "config file path")
	root.PersistentFlags().StringVar(&a.envFilePath, "env-file", "", "env file path (fallback: ./.rascal.env if present)")
	root.PersistentFlags().StringVar(&a.serverURLFlag, "server-url", "", "orchestrator URL")
	root.PersistentFlags().StringVar(&a.apiTokenFlag, "api-token", "", "orchestrator API token")
	root.PersistentFlags().StringVar(&a.defaultRepoFlag, "default-repo", "", "default repository in OWNER/REPO form")
	root.PersistentFlags().StringVar(&a.transportFlag, "transport", "", "API transport: auto|http|ssh")
	root.PersistentFlags().StringVar(&a.sshHostFlag, "client-ssh-host", "", "SSH target host for API transport=ssh/auto")
	root.PersistentFlags().StringVar(&a.sshUserFlag, "client-ssh-user", "", "SSH target user for API transport=ssh/auto")
	root.PersistentFlags().StringVar(&a.sshKeyFlag, "client-ssh-key", "", "SSH private key path for API transport=ssh/auto")
	root.PersistentFlags().IntVar(&a.sshPortFlag, "client-ssh-port", 0, "SSH target port for API transport=ssh/auto")
	root.PersistentFlags().StringVar(&a.output, "output", "table", "output format: table|json|toml")
	root.PersistentFlags().BoolVar(&a.noColor, "no-color", false, "disable ANSI color/style output (also set by NO_COLOR)")
	root.PersistentFlags().BoolVarP(&a.quiet, "quiet", "q", false, "reduce non-essential output")

	initCmd := a.newInitCmd()
	initCmd.GroupID = "setup"
	bootstrapCmd := a.newBootstrapCmd()
	bootstrapCmd.GroupID = "setup"
	deployCmd := a.newDeployCmd()
	deployCmd.GroupID = "setup"
	configCmd := a.newConfigCmd()
	configCmd.GroupID = "setup"
	authCmd := a.newAuthCmd()
	authCmd.GroupID = "setup"

	runCmd := a.newRunCmd()
	runCmd.GroupID = "runs"
	psCmd := a.newPSCmd()
	psCmd.GroupID = "runs"
	openCmd := a.newOpenCmd()
	openCmd.GroupID = "runs"
	retryCmd := a.newRetryCmd()
	retryCmd.GroupID = "runs"
	cancelCmd := a.newCancelCmd()
	cancelCmd.GroupID = "runs"
	taskCmd := a.newTaskCmd()
	taskCmd.GroupID = "runs"

	logsCmd := a.newLogsCmd()
	logsCmd.GroupID = "logs"

	githubCmd := a.newGitHubCmd()
	githubCmd.GroupID = "integrations"
	infraCmd := a.newInfraCmd()
	infraCmd.GroupID = "integrations"

	doctorCmd := a.newDoctorCmd()
	doctorCmd.GroupID = "utilities"
	completionCmd := newCompletionCmd(root)
	completionCmd.GroupID = "utilities"

	root.AddCommand(initCmd)
	root.AddCommand(bootstrapCmd)
	root.AddCommand(deployCmd)
	root.AddCommand(configCmd)
	root.AddCommand(authCmd)
	root.AddCommand(runCmd)
	root.AddCommand(psCmd)
	root.AddCommand(openCmd)
	root.AddCommand(retryCmd)
	root.AddCommand(cancelCmd)
	root.AddCommand(taskCmd)
	root.AddCommand(logsCmd)
	root.AddCommand(githubCmd)
	root.AddCommand(infraCmd)
	root.AddCommand(doctorCmd)
	root.AddCommand(completionCmd)

	return root
}

func (a *app) initConfig() error {
	v := viper.New()
	v.SetConfigFile(a.configPath)
	v.SetConfigType("toml")
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
		Host:        strings.TrimSpace(v.GetString("host")),
		Domain:      strings.TrimSpace(v.GetString("domain")),
		Transport:   strings.TrimSpace(v.GetString("transport")),
		SSHHost:     strings.TrimSpace(v.GetString("ssh_host")),
		SSHUser:     strings.TrimSpace(v.GetString("ssh_user")),
		SSHKey:      strings.TrimSpace(v.GetString("ssh_key")),
		SSHPort:     v.GetInt("ssh_port"),
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
	if strings.TrimSpace(a.transportFlag) != "" {
		a.cfg.Transport = strings.ToLower(strings.TrimSpace(a.transportFlag))
		a.transportSource = "flag"
	} else if strings.TrimSpace(os.Getenv("RASCAL_TRANSPORT")) != "" {
		a.transportSource = "env"
	} else if v.InConfig("transport") {
		a.transportSource = "config"
	} else {
		a.transportSource = "default"
	}
	if strings.TrimSpace(a.sshHostFlag) != "" {
		a.cfg.SSHHost = strings.TrimSpace(a.sshHostFlag)
	}
	if strings.TrimSpace(a.sshUserFlag) != "" {
		a.cfg.SSHUser = strings.TrimSpace(a.sshUserFlag)
	}
	if strings.TrimSpace(a.sshKeyFlag) != "" {
		a.cfg.SSHKey = strings.TrimSpace(a.sshKeyFlag)
	}
	if a.sshPortFlag > 0 {
		a.cfg.SSHPort = a.sshPortFlag
	}

	a.cfg.ServerURL = strings.TrimRight(a.cfg.ServerURL, "/")
	if a.cfg.ServerURL == "" {
		a.cfg.ServerURL = "http://127.0.0.1:8080"
	}
	if a.cfg.Transport == "" {
		a.cfg.Transport = "auto"
	}
	if a.cfg.SSHHost == "" {
		a.cfg.SSHHost = strings.TrimSpace(a.cfg.Host)
	}
	if a.cfg.SSHUser == "" {
		a.cfg.SSHUser = "root"
	}
	if a.cfg.SSHPort <= 0 {
		a.cfg.SSHPort = 22
	}
	resolvedTransport := resolveTransport(a.cfg.Transport, a.cfg.ServerURL, a.cfg.SSHHost)

	a.client = apiClient{
		baseURL:   a.cfg.ServerURL,
		token:     a.cfg.APIToken,
		http:      &http.Client{Timeout: 30 * time.Second},
		transport: resolvedTransport,
		sshHost:   strings.TrimSpace(a.cfg.SSHHost),
		sshUser:   strings.TrimSpace(a.cfg.SSHUser),
		sshKey:    strings.TrimSpace(a.cfg.SSHKey),
		sshPort:   a.cfg.SSHPort,
	}
	if a.transportSource == "default" {
		a.transportSource = "resolved"
	}

	switch strings.ToLower(strings.TrimSpace(a.output)) {
	case "", "table":
		a.output = "table"
	case "json", "toml":
	default:
		return &cliError{
			Code:    exitInput,
			Message: fmt.Sprintf("unsupported --output value %q", a.output),
			Hint:    "use --output table|json|toml",
		}
	}
	switch strings.ToLower(strings.TrimSpace(a.cfg.Transport)) {
	case "", "auto", "http", "ssh":
	default:
		return &cliError{
			Code:    exitInput,
			Message: fmt.Sprintf("unsupported transport %q", a.cfg.Transport),
			Hint:    "use --transport auto|http|ssh",
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
	case "toml":
		data, err := toml.Marshal(v)
		if err != nil {
			return err
		}
		_, err = os.Stdout.Write(data)
		return err
	default:
		return &cliError{Code: exitInput, Message: "invalid output format", Hint: "use table|json|toml"}
	}
}

func (a *app) requireServerAuth() error {
	if strings.TrimSpace(a.cfg.APIToken) != "" {
		return nil
	}
	return &cliError{
		Code:    exitConfig,
		Message: "missing API token",
		Hint:    "set RASCAL_API_TOKEN, configure ~/.rascal/config.toml, or run `rascal init`",
	}
}

func (a *app) newInitCmd() *cobra.Command {
	var (
		serverURL      string
		apiToken       string
		defaultRepo    string
		host           string
		domain         string
		transport      string
		sshHost        string
		sshUser        string
		sshKey         string
		sshPort        int
		nonInteractive bool
	)

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Initialize local Rascal CLI config",
		Long:  "Create or update local Rascal config at ~/.rascal/config.toml (or --config path).",
		RunE: func(_ *cobra.Command, _ []string) error {
			serverURL = firstNonEmpty(strings.TrimSpace(serverURL), a.cfg.ServerURL)
			apiToken = firstNonEmpty(strings.TrimSpace(apiToken), a.cfg.APIToken)
			defaultRepo = firstNonEmpty(strings.TrimSpace(defaultRepo), a.cfg.DefaultRepo)
			host = firstNonEmpty(strings.TrimSpace(host), a.cfg.Host)
			domain = firstNonEmpty(strings.TrimSpace(domain), a.cfg.Domain)
			transport = firstNonEmpty(strings.ToLower(strings.TrimSpace(transport)), a.cfg.Transport)
			sshHost = firstNonEmpty(strings.TrimSpace(sshHost), a.cfg.SSHHost, host)
			sshUser = firstNonEmpty(strings.TrimSpace(sshUser), a.cfg.SSHUser, "root")
			sshKey = firstNonEmpty(strings.TrimSpace(sshKey), a.cfg.SSHKey)
			if sshPort <= 0 {
				sshPort = a.cfg.SSHPort
			}
			if sshPort <= 0 {
				sshPort = 22
			}

			if !nonInteractive && a.isTTY() {
				reader := bufio.NewReader(os.Stdin)
				serverURL = promptString(reader, "Server URL", serverURL)
				apiToken = promptString(reader, "API Token", apiToken)
				defaultRepo = promptString(reader, "Default Repo (optional)", defaultRepo)
				host = promptString(reader, "Host (optional)", host)
				domain = promptString(reader, "Domain (optional)", domain)
				transport = promptString(reader, "Transport (auto|http|ssh)", transport)
				sshHost = promptString(reader, "SSH Host (optional)", sshHost)
				sshUser = promptString(reader, "SSH User (optional)", sshUser)
				sshKey = promptString(reader, "SSH Key (optional)", sshKey)
			}

			if strings.TrimSpace(serverURL) == "" {
				return &cliError{Code: exitInput, Message: "server URL is required", Hint: "pass --server-url or run interactively"}
			}
			transport = strings.ToLower(strings.TrimSpace(transport))
			if transport == "" {
				transport = "auto"
			}
			switch transport {
			case "auto", "http", "ssh":
			default:
				return &cliError{Code: exitInput, Message: "invalid transport", Hint: "transport must be one of: auto|http|ssh"}
			}

			cfg := config.ClientConfig{
				ServerURL:   strings.TrimRight(serverURL, "/"),
				APIToken:    strings.TrimSpace(apiToken),
				DefaultRepo: strings.TrimSpace(defaultRepo),
				Host:        strings.TrimSpace(host),
				Domain:      strings.TrimSpace(domain),
				Transport:   transport,
				SSHHost:     strings.TrimSpace(sshHost),
				SSHUser:     firstNonEmpty(strings.TrimSpace(sshUser), "root"),
				SSHKey:      strings.TrimSpace(sshKey),
				SSHPort:     sshPort,
			}
			if err := config.SaveClientConfig(a.configPath, cfg); err != nil {
				return &cliError{Code: exitConfig, Message: "failed to write config", Hint: "check file permissions", Cause: err}
			}
			a.println("config written: %s", a.configPath)
			return nil
		},
	}
	cmd.Flags().StringVar(&serverURL, "server-url", "", "orchestrator URL")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "orchestrator API token")
	cmd.Flags().StringVar(&defaultRepo, "default-repo", "", "default repository in OWNER/REPO form")
	cmd.Flags().StringVar(&host, "host", "", "default server host/IP for bootstrap/deploy")
	cmd.Flags().StringVar(&domain, "domain", "", "default domain for server URL and Caddy")
	cmd.Flags().StringVar(&transport, "transport", "", "default transport: auto|http|ssh")
	cmd.Flags().StringVar(&sshHost, "ssh-host", "", "default SSH target host for transport=ssh/auto")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "", "default SSH target user for transport=ssh/auto")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "default SSH private key path for transport=ssh/auto")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 0, "default SSH target port for transport=ssh/auto")
	cmd.Flags().BoolVar(&nonInteractive, "non-interactive", false, "disable prompts and rely on flags")
	return cmd
}

func (a *app) newBootstrapCmd() *cobra.Command {
	var (
		repo               string
		domain             string
		serverURL          string
		apiToken           string
		githubAdminToken   string
		githubRuntimeToken string
		webhookSecret      string
		skipWebhook        bool
		writeConfig        bool
		host               string
		sshUser            string
		sshKey             string
		sshPort            int
		skipDeploy         bool
		provisionNew       bool
		codexAuthPath      string
		hcloudToken        string
		printPlan          bool
	)

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Configure client, provision (optional), and deploy orchestrator",
		RunE: func(_ *cobra.Command, _ []string) error {
			repo = firstNonEmpty(strings.TrimSpace(repo), a.cfg.DefaultRepo)
			domain = firstNonEmpty(strings.TrimSpace(domain), strings.TrimSpace(a.cfg.Domain))
			serverURL = strings.TrimSpace(serverURL)
			apiToken = firstNonEmpty(strings.TrimSpace(apiToken), strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")))
			githubAdminToken = firstNonEmpty(strings.TrimSpace(githubAdminToken), strings.TrimSpace(os.Getenv("GITHUB_ADMIN_TOKEN")), strings.TrimSpace(os.Getenv("GITHUB_TOKEN")))
			githubRuntimeToken = firstNonEmpty(strings.TrimSpace(githubRuntimeToken), strings.TrimSpace(os.Getenv("GITHUB_RUNTIME_TOKEN")), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_RUNTIME_TOKEN")))
			webhookSecret = firstNonEmpty(strings.TrimSpace(webhookSecret), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_WEBHOOK_SECRET")), strings.TrimSpace(os.Getenv("GITHUB_WEBHOOK_SECRET")))
			host = firstNonEmpty(strings.TrimSpace(host), strings.TrimSpace(a.cfg.Host))
			sshUser = strings.TrimSpace(sshUser)
			sshKey = strings.TrimSpace(sshKey)
			codexAuthPath = strings.TrimSpace(codexAuthPath)
			hcloudToken = firstNonEmpty(strings.TrimSpace(hcloudToken), strings.TrimSpace(os.Getenv("HCLOUD_TOKEN")))

			if provisionNew {
				host = ""
			}
			shouldDeploy := !skipDeploy && (host != "" || hcloudToken != "")
			willProvision := shouldDeploy && host == ""
			resolvedServerURL, resolvedServerURLSource := resolveBootstrapServerURL(serverURL, domain, host, a.cfg.ServerURL, willProvision)
			missing := make([]string, 0, 8)
			if provisionNew && hcloudToken == "" {
				missing = append(missing, "Hetzner token is required with --provision-new (--hcloud-token or HCLOUD_TOKEN)")
			}
			if shouldDeploy && sshPort <= 0 {
				missing = append(missing, "SSH port must be positive (--ssh-port)")
			}
			if !skipWebhook && repo == "" {
				missing = append(missing, "repository is required when webhook setup is enabled (--repo OWNER/REPO)")
			}
			if !skipWebhook && githubAdminToken == "" {
				missing = append(missing, "GitHub admin token is required for webhook setup (--github-admin-token or GITHUB_ADMIN_TOKEN)")
			}
			if shouldDeploy && githubRuntimeToken == "" {
				missing = append(missing, "GitHub runtime token is required for deployment (--github-runtime-token or GITHUB_RUNTIME_TOKEN)")
			}
			if err := validateDistinctGitHubTokens(githubAdminToken, githubRuntimeToken, !skipWebhook && shouldDeploy); err != nil {
				missing = append(missing, err.Error())
			}
			if shouldDeploy {
				if strings.TrimSpace(codexAuthPath) == "" {
					missing = append(missing, "codex auth file path is required for deployment (--codex-auth)")
				} else if expandedAuthPath, err := expandPath(codexAuthPath); err != nil {
					missing = append(missing, fmt.Sprintf("codex auth path cannot be expanded (%s): %v", codexAuthPath, err))
				} else if _, err := os.Stat(expandedAuthPath); err != nil {
					missing = append(missing, fmt.Sprintf("codex auth file not found: %s", expandedAuthPath))
				}
			}
			if resolvedServerURL == "" {
				missing = append(missing, "server URL cannot be derived (set --server-url, --domain, --host, or config server_url)")
			}
			actions := make([]string, 0, 4)
			if shouldDeploy {
				if willProvision {
					actions = append(actions, "provision a new Hetzner host")
				}
				deployHost := host
				if deployHost == "" {
					deployHost = "<provisioned-host>"
				}
				actions = append(actions, fmt.Sprintf("deploy rascald to %s over SSH", deployHost))
			}
			if !skipWebhook {
				webhookRepo := firstNonEmpty(repo, "<missing repo>")
				actions = append(actions, fmt.Sprintf("configure GitHub webhook and label for %s", webhookRepo))
			}
			if writeConfig {
				actions = append(actions, fmt.Sprintf("write local config: %s", a.configPath))
			}
			if len(actions) == 0 {
				actions = append(actions, "no-op (deploy and webhook setup are both disabled)")
			}
			if printPlan {
				ready := len(missing) == 0
				planOut := map[string]any{
					"status":  "bootstrap_plan",
					"ready":   ready,
					"actions": actions,
					"missing": missing,
					"resolved": map[string]any{
						"repo":                         repo,
						"host":                         host,
						"domain":                       domain,
						"server_url":                   resolvedServerURL,
						"server_url_source":            resolvedServerURLSource,
						"deploy_enabled":               shouldDeploy,
						"provision_new_host":           willProvision,
						"webhook_enabled":              !skipWebhook,
						"write_config":                 writeConfig,
						"ssh_user":                     firstNonEmpty(sshUser, "root"),
						"ssh_port":                     sshPort,
						"api_token_present":            strings.TrimSpace(apiToken) != "",
						"github_admin_token_present":   strings.TrimSpace(githubAdminToken) != "",
						"github_runtime_token_present": strings.TrimSpace(githubRuntimeToken) != "",
						"webhook_secret_present":       strings.TrimSpace(webhookSecret) != "",
						"hcloud_token_present":         strings.TrimSpace(hcloudToken) != "",
						"codex_auth_path":              codexAuthPath,
					},
				}
				return a.emit(planOut, func() error {
					a.println("bootstrap plan")
					if ready {
						a.println("ready: yes")
					} else {
						a.println("ready: no")
					}
					a.println("server_url: %s (%s)", firstNonEmpty(resolvedServerURL, "<unresolved>"), resolvedServerURLSource)
					a.println("actions:")
					for _, action := range actions {
						a.println("- %s", action)
					}
					if len(missing) > 0 {
						a.println("missing prerequisites:")
						for _, item := range missing {
							a.println("- %s", item)
						}
					}
					return nil
				})
			}

			if provisionNew && hcloudToken == "" {
				return fmt.Errorf("--hcloud-token is required when --provision-new is set")
			}
			if shouldDeploy && sshPort <= 0 {
				return fmt.Errorf("--ssh-port must be positive")
			}
			if !skipWebhook && repo == "" {
				return fmt.Errorf("--repo is required when webhook setup is enabled")
			}
			if !skipWebhook && githubAdminToken == "" {
				return fmt.Errorf("--github-admin-token is required when webhook setup is enabled")
			}
			if shouldDeploy && githubRuntimeToken == "" {
				return fmt.Errorf("--github-runtime-token is required when deployment is enabled")
			}
			if err := validateDistinctGitHubTokens(githubAdminToken, githubRuntimeToken, !skipWebhook && shouldDeploy); err != nil {
				return err
			}

			var expandedAuthPath string
			if shouldDeploy {
				if codexAuthPath == "" {
					return fmt.Errorf("--codex-auth must be set when deployment is enabled")
				}
				var err error
				expandedAuthPath, err = expandPath(codexAuthPath)
				if err != nil {
					return fmt.Errorf("expand codex auth path: %w", err)
				}
				if _, err := os.Stat(expandedAuthPath); err != nil {
					return fmt.Errorf("codex auth file is required at %s: %w", expandedAuthPath, err)
				}
			}

			var provisionOut *hcloudProvisionResult
			if shouldDeploy && host == "" {
				if hcloudToken == "" {
					return fmt.Errorf("no host configured: pass --host, configure `host` in config, or set --hcloud-token")
				}
				cfg, timeout, err := resolveHetznerProvisionConfig(hetznerProvisionInput{
					Token:         hcloudToken,
					ApplyFirewall: true,
				})
				if err != nil {
					return fmt.Errorf("expand default ssh public key path: %w", err)
				}
				out, err := runHetznerProvision(cfg, timeout)
				if err != nil {
					return fmt.Errorf("hcloud provision: %w", err)
				}
				host = strings.TrimSpace(out.Host)
				provisionOut = &out
			}

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

			deployPerformed := false
			if shouldDeploy {
				input := deployExistingInput{
					Host:               host,
					SSHUser:            sshUser,
					SSHKey:             sshKey,
					SSHPort:            sshPort,
					ProvisionedArch:    "",
					APIToken:           apiToken,
					GitHubRuntimeToken: githubRuntimeToken,
					WebhookSecret:      webhookSecret,
					CodexAuthPath:      expandedAuthPath,
					Domain:             domain,
					SkipEnvUpload:      false,
					SkipAuthUpload:     false,
					SkipIfHealthy:      true,
					RawErrors:          true,
				}
				if provisionOut != nil {
					input.ProvisionedArch = provisionOut.Architecture
				}
				result, err := a.runDeployExisting(input)
				if err != nil {
					return err
				}
				deployPerformed = result.DeployPerformed
				if err := waitForServerHealthSSH(deployConfig{
					Host:       host,
					SSHUser:    firstNonEmpty(sshUser, "root"),
					SSHKeyPath: sshKey,
					SSHPort:    sshPort,
				}, 90*time.Second); err != nil {
					return fmt.Errorf("server health check failed after bootstrap deploy stage: %w", err)
				}
				if deployPerformed {
					a.println("deployed rascald to %s", host)
				}
			}

			if serverURL == "" {
				switch {
				case domain != "":
					serverURL = "https://" + domain
				case host != "":
					serverURL = "http://" + host + ":8080"
				case strings.TrimSpace(a.cfg.ServerURL) != "" && strings.TrimSpace(a.cfg.ServerURL) != "http://127.0.0.1:8080":
					serverURL = strings.TrimSpace(a.cfg.ServerURL)
				default:
					return fmt.Errorf("either --server-url, --domain, or a provisioned/explicit --host is required")
				}
			}
			serverURL = strings.TrimRight(serverURL, "/")

			if !skipWebhook {
				if _, err := a.runRepoEnable(repoEnableInput{
					Repo:          repo,
					GitHubToken:   githubAdminToken,
					WebhookSecret: webhookSecret,
					WebhookURL:    serverURL + "/v1/webhooks/github",
					Timeout:       45 * time.Second,
					RawErrors:     true,
				}); err != nil {
					return err
				}
			}

			if writeConfig {
				save := a.cfg
				save.ServerURL = serverURL
				save.APIToken = apiToken
				save.DefaultRepo = repo
				if host != "" {
					save.Host = host
					save.SSHHost = host
				}
				if domain != "" {
					save.Domain = domain
				}
				if strings.TrimSpace(sshUser) != "" {
					save.SSHUser = strings.TrimSpace(sshUser)
				}
				if strings.TrimSpace(sshKey) != "" {
					save.SSHKey = strings.TrimSpace(sshKey)
				}
				if sshPort > 0 {
					save.SSHPort = sshPort
				}
				if strings.TrimSpace(save.Transport) == "" {
					save.Transport = "auto"
				}
				if err := config.SaveClientConfig(a.configPath, save); err != nil {
					return err
				}
			}

			out := map[string]any{
				"status":         "bootstrap_complete",
				"server_url":     serverURL,
				"api_token":      maskSecret(apiToken),
				"default_repo":   repo,
				"webhook_secret": maskSecret(webhookSecret),
				"host":           host,
				"domain":         domain,
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
				a.println("host: %s", host)
				if domain != "" {
					a.println("domain: %s", domain)
				}
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
	cmd.Flags().StringVar(&serverURL, "server-url", "", "orchestrator URL (overrides --domain)")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "API token for orchestrator (auto-generated if empty)")
	cmd.Flags().StringVar(&githubAdminToken, "github-admin-token", "", "GitHub token with repo Webhooks (rw) and Issues (rw) for label/webhook setup")
	cmd.Flags().StringVar(&githubRuntimeToken, "github-runtime-token", "", "GitHub token for remote runner operations (push/PR)")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret (auto-generated if empty)")
	cmd.Flags().BoolVar(&skipWebhook, "skip-webhook", false, "skip GitHub webhook setup")
	cmd.Flags().BoolVar(&writeConfig, "write-config", true, "write config file")
	cmd.Flags().StringVar(&host, "host", "", "existing server host (defaults to config `host` if set)")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH target user for existing host deployment")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path for existing host deployment")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH target port for existing host deployment")
	cmd.Flags().BoolVar(&skipDeploy, "skip-deploy", false, "skip remote deployment")
	cmd.Flags().BoolVar(&provisionNew, "provision-new", false, "force provisioning a new host when --hcloud-token is set")
	cmd.Flags().StringVar(&codexAuthPath, "codex-auth", "~/.codex/auth.json", "local Codex auth.json path copied to the server")
	cmd.Flags().StringVar(&hcloudToken, "hcloud-token", "", "Hetzner Cloud token (or HCLOUD_TOKEN) for provisioning")
	cmd.Flags().BoolVar(&printPlan, "print-plan", false, "print resolved bootstrap actions and prerequisites without executing")

	return cmd
}

func (a *app) newDeployCmd() *cobra.Command {
	cmd := a.newDeployExistingCmd("deploy", "Deploy rascald to an existing host")
	cmd.Long = "Deploy or redeploy rascald to an existing Linux host over SSH without running provisioning or webhook setup."
	cmd.Example = strings.TrimSpace(`
rascal deploy --host 203.0.113.10
rascal deploy --host 203.0.113.10 --upload-env --github-runtime-token "$GITHUB_RUNTIME_TOKEN" --upload-auth --codex-auth ~/.codex/auth.json
`)
	return cmd
}

func (a *app) newRunCmd() *cobra.Command {
	var repo, task, baseBranch, issueRef string
	var debug bool
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start a run",
		Long:  "Start a run for a repository/task pair, or create a run from an issue reference. The repository must be in OWNER/REPO form.",
		Example: strings.TrimSpace(`
rascal run -R OWNER/REPO -t "Fix flaky tests"
rascal run --repo OWNER/REPO --task "Refactor parser" --output json
rascal run --issue OWNER/REPO#123
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			issueRef = strings.TrimSpace(issueRef)
			if issueRef != "" {
				if cmd.Flags().Changed("repo") || cmd.Flags().Changed("task") || cmd.Flags().Changed("base-branch") {
					return &cliError{Code: exitInput, Message: "--issue cannot be combined with --repo, --task, or --base-branch"}
				}
				repo, issueNumber, err := parseIssueRef(issueRef)
				if err != nil {
					return &cliError{Code: exitInput, Message: err.Error()}
				}
				payload := map[string]any{
					"repo":         repo,
					"issue_number": issueNumber,
					"debug":        debug,
				}
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
			}
			var inferredRepo bool
			repo, inferredRepo = resolveRepo(strings.TrimSpace(repo), a.cfg.DefaultRepo, inferRepoFromGitRemote)
			task = strings.TrimSpace(task)
			baseBranch = firstNonEmpty(strings.TrimSpace(baseBranch), "main")
			if repo == "" || task == "" {
				return &cliError{Code: exitInput, Message: "both --repo/-R and --task/-t are required"}
			}

			payload := map[string]any{
				"repo":        repo,
				"task":        task,
				"base_branch": baseBranch,
				"debug":       debug,
			}
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
				if inferredRepo {
					a.println("hint: using repo from git remote: %s", repo)
				}
				a.println("run created: %s (%s)", out.Run.ID, out.Run.Status)
				return nil
			})
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "R", "", "repository in OWNER/REPO form")
	cmd.Flags().StringVarP(&task, "task", "t", "", "task text")
	cmd.Flags().StringVarP(&baseBranch, "base-branch", "b", "main", "base branch")
	cmd.Flags().StringVar(&issueRef, "issue", "", "issue reference in OWNER/REPO#NUMBER form")
	cmd.Flags().BoolVar(&debug, "debug", true, "stream detailed agent execution logs (use --debug=false to reduce verbosity)")
	return cmd
}

func (a *app) newPSCmd() *cobra.Command {
	var (
		limit    int
		all      bool
		watch    bool
		interval time.Duration
	)
	cmd := &cobra.Command{
		Use:     "ps",
		Aliases: []string{"ls"},
		Short:   "List recent runs",
		Example: strings.TrimSpace(`
  rascal ps
  rascal ps --limit 25
  rascal ps --all
  rascal ps --watch
  rascal ps --output json
`),
		RunE: func(c *cobra.Command, _ []string) error {
			if err := a.requireServerAuth(); err != nil {
				return err
			}
			if all && c.Flags().Changed("limit") {
				return &cliError{Code: exitInput, Message: "--all cannot be combined with --limit"}
			}
			if watch && a.output != "table" {
				return &cliError{Code: exitInput, Message: "--watch is only supported with --output table"}
			}

			render := func(runs []state.Run) error {
				return a.emit(map[string]any{"runs": runs}, func() error {
					tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
					fmt.Fprintln(tw, "RUN ID\tSTATUS\tREPO\tPR\tCREATED (UTC)")
					for _, run := range runs {
						fmt.Fprintf(
							tw,
							"%s\t%s\t%s\t%s\t%s\n",
							run.ID,
							psStatusLabel(run),
							run.Repo,
							psPRLabel(run),
							psCreatedLabel(run.CreatedAt),
						)
					}
					return tw.Flush()
				})
			}

			if !watch {
				runs, err := a.fetchRuns(limit, all)
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
				runs, err := a.fetchRuns(limit, all)
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
	cmd.Flags().IntVar(&limit, "limit", 10, "max number of runs")
	cmd.Flags().BoolVar(&all, "all", false, "show all retained runs")
	cmd.Flags().BoolVar(&watch, "watch", false, "refresh continuously")
	cmd.Flags().DurationVar(&interval, "interval", 2*time.Second, "refresh interval when --watch is enabled")
	return cmd
}

func (a *app) newLogsCmd() *cobra.Command {
	var (
		follow   bool
		interval time.Duration
		since    time.Duration
		lines    int
	)
	cmd := &cobra.Command{
		Use:     "logs [run_id]",
		Aliases: []string{"tail"},
		Short:   "Fetch run/service logs",
		Long:    "Fetch run logs and service logs. Canonical run-log usage is `rascal logs run <run_id>`; `rascal logs <run_id>` is a shorthand.",
		Example: strings.TrimSpace(`
rascal logs run run_abc123
rascal logs run run_abc123 --follow
rascal logs rascald --follow
rascal logs caddy-access --follow
`),
		Args:              cobra.MaximumNArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(c *cobra.Command, args []string) error {
			if len(args) == 0 {
				return c.Help()
			}
			return a.streamRunLogs(args[0], follow, interval, since, lines)
		},
	}
	cmd.Flags().BoolVar(&follow, "follow", false, "stream logs by polling")
	cmd.Flags().DurationVar(&interval, "interval", 4*time.Second, "polling interval when --follow is enabled")
	cmd.Flags().DurationVar(&since, "since", 0, "show logs since duration ago (best effort)")
	cmd.Flags().IntVar(&lines, "lines", 200, "max log lines to fetch")

	runCmd := &cobra.Command{
		Use:     "run <run_id>",
		Aliases: []string{"job"},
		Short:   "Fetch logs for a run",
		Long:    "Fetch logs for a specific run ID.",
		Example: strings.TrimSpace(`
rascal logs run run_abc123
rascal logs run run_abc123 --follow --interval 4s
`),
		Args:              cobra.ExactArgs(1),
		ValidArgsFunction: a.runIDCompletion,
		RunE: func(cmd *cobra.Command, args []string) error {
			runFollow, _ := cmd.Flags().GetBool("follow")
			runInterval, _ := cmd.Flags().GetDuration("interval")
			runSince, _ := cmd.Flags().GetDuration("since")
			runLines, _ := cmd.Flags().GetInt("lines")
			return a.streamRunLogs(args[0], runFollow, runInterval, runSince, runLines)
		},
	}
	runCmd.Flags().Bool("follow", false, "stream logs by polling")
	runCmd.Flags().Duration("interval", 4*time.Second, "polling interval when --follow is enabled")
	runCmd.Flags().Duration("since", 0, "show logs since duration ago (best effort)")
	runCmd.Flags().Int("lines", 200, "max log lines to fetch")

	rascaldCmd := &cobra.Command{
		Use:   "rascald",
		Short: "Fetch rascald system logs over SSH",
		Long:  "Stream logs from the remote `rascal`/`rascal@slot` systemd service over SSH.",
		Example: strings.TrimSpace(`
rascal logs rascald
rascal logs rascald --follow
rascal logs rascald --host rascal-server
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, _ := cmd.Flags().GetString("host")
			sshUser, _ := cmd.Flags().GetString("ssh-user")
			sshKey, _ := cmd.Flags().GetString("ssh-key")
			sshPort, _ := cmd.Flags().GetInt("ssh-port")
			serviceFollow, _ := cmd.Flags().GetBool("follow")
			serviceLines, _ := cmd.Flags().GetInt("lines")
			return a.streamRascaldServiceLogs(host, sshUser, sshKey, sshPort, serviceFollow, serviceLines)
		},
	}

	caddyCmd := &cobra.Command{
		Use:   "caddy",
		Short: "Fetch Caddy system logs over SSH",
		Long:  "Stream logs from the remote Caddy systemd service over SSH.",
		Example: strings.TrimSpace(`
rascal logs caddy
rascal logs caddy --follow
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, _ := cmd.Flags().GetString("host")
			sshUser, _ := cmd.Flags().GetString("ssh-user")
			sshKey, _ := cmd.Flags().GetString("ssh-key")
			sshPort, _ := cmd.Flags().GetInt("ssh-port")
			serviceFollow, _ := cmd.Flags().GetBool("follow")
			serviceLines, _ := cmd.Flags().GetInt("lines")
			return a.streamSystemdUnitLogs("caddy", host, sshUser, sshKey, sshPort, serviceFollow, serviceLines)
		},
	}

	caddyAccessCmd := &cobra.Command{
		Use:     "caddy-access",
		Aliases: []string{"caddy-access-log"},
		Short:   "Fetch Caddy access log file over SSH",
		Long:    "Stream `/var/log/caddy/rascal-access.log` over SSH.",
		Example: strings.TrimSpace(`
rascal logs caddy-access
rascal logs caddy-access --follow
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			host, _ := cmd.Flags().GetString("host")
			sshUser, _ := cmd.Flags().GetString("ssh-user")
			sshKey, _ := cmd.Flags().GetString("ssh-key")
			sshPort, _ := cmd.Flags().GetInt("ssh-port")
			serviceFollow, _ := cmd.Flags().GetBool("follow")
			serviceLines, _ := cmd.Flags().GetInt("lines")
			return a.streamRemoteFileLogs("/var/log/caddy/rascal-access.log", host, sshUser, sshKey, sshPort, serviceFollow, serviceLines)
		},
	}

	addServiceFlags := func(sub *cobra.Command) {
		sub.Flags().String("host", "", "remote host (default: ssh_host/host from config)")
		sub.Flags().String("ssh-user", "", "SSH target user (default: configured ssh_user or root)")
		sub.Flags().String("ssh-key", "", "SSH private key path")
		sub.Flags().Int("ssh-port", 0, "SSH target port (default: configured ssh_port or 22)")
		sub.Flags().Bool("follow", false, "follow logs continuously")
		sub.Flags().Int("lines", 200, "number of recent lines to show first")
	}
	addServiceFlags(rascaldCmd)
	addServiceFlags(caddyCmd)
	addServiceFlags(caddyAccessCmd)

	cmd.AddCommand(runCmd)
	cmd.AddCommand(rascaldCmd)
	cmd.AddCommand(caddyCmd)
	cmd.AddCommand(caddyAccessCmd)
	return cmd
}

func (a *app) streamRunLogs(runID string, follow bool, interval, since time.Duration, lines int) error {
	if err := a.requireServerAuth(); err != nil {
		return err
	}
	if lines <= 0 {
		lines = 200
	}
	if interval <= 0 {
		interval = 4 * time.Second
	}

	fetch := func(path string) (*http.Response, error) {
		resp, err := a.client.do(http.MethodGet, path, nil)
		if err != nil {
			return nil, &cliError{Code: exitServer, Message: "request failed", Cause: err}
		}
		if resp.StatusCode >= 300 {
			defer resp.Body.Close()
			return nil, decodeServerError(resp)
		}
		return resp, nil
	}

	fetchText := func() (string, error) {
		path := fmt.Sprintf("/v1/runs/%s/logs?lines=%d", url.PathEscape(strings.TrimSpace(runID)), lines)
		resp, err := fetch(path)
		if err != nil {
			return "", err
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", err
		}
		return string(body), nil
	}

	if !follow {
		body, err := fetchText()
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
	type followResponse struct {
		Logs      string          `json:"logs"`
		RunStatus state.RunStatus `json:"run_status"`
		Done      bool            `json:"done"`
	}

	fetchFollow := func() (followResponse, error) {
		path := fmt.Sprintf("/v1/runs/%s/logs?lines=%d&format=json", url.PathEscape(strings.TrimSpace(runID)), lines)
		resp, err := fetch(path)
		if err != nil {
			return followResponse{}, err
		}
		defer resp.Body.Close()
		var out followResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return followResponse{}, &cliError{Code: exitServer, Message: "failed to decode server response", Cause: err}
		}
		return out, nil
	}

	last := ""
	for {
		payload, err := fetchFollow()
		if err != nil {
			return err
		}
		body := payload.Logs
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
		if payload.Done {
			return nil
		}
		select {
		case <-sigCtx.Done():
			return nil
		case <-time.After(interval):
		}
	}
}

func (a *app) resolveSSHLogConfig(host, sshUser, sshKey string, sshPort int) (deployConfig, error) {
	resolvedHost := firstNonEmpty(strings.TrimSpace(host), strings.TrimSpace(a.cfg.SSHHost), strings.TrimSpace(a.cfg.Host))
	if resolvedHost == "" {
		return deployConfig{}, &cliError{Code: exitInput, Message: "missing SSH host", Hint: "set --host or configure ssh_host/host"}
	}
	return deployConfig{
		Host:       resolvedHost,
		SSHUser:    firstNonEmpty(strings.TrimSpace(sshUser), strings.TrimSpace(a.cfg.SSHUser), "root"),
		SSHKeyPath: firstNonEmpty(strings.TrimSpace(sshKey), strings.TrimSpace(a.cfg.SSHKey)),
		SSHPort:    firstPositive(sshPort, a.cfg.SSHPort, 22),
	}, nil
}

func (a *app) streamSystemdUnitLogs(unit, host, sshUser, sshKey string, sshPort int, follow bool, lines int) error {
	cfg, err := a.resolveSSHLogConfig(host, sshUser, sshKey, sshPort)
	if err != nil {
		return err
	}
	if lines <= 0 {
		lines = 200
	}
	args := []string{"journalctl", "-u", shellSingleQuote(strings.TrimSpace(unit)), "--no-pager", "-n", fmt.Sprintf("%d", lines)}
	if follow {
		args = append(args, "-f")
	}
	return runLocal("ssh", sshArgs(cfg, strings.Join(args, " "))...)
}

func (a *app) streamRascaldServiceLogs(host, sshUser, sshKey string, sshPort int, follow bool, lines int) error {
	cfg, err := a.resolveSSHLogConfig(host, sshUser, sshKey, sshPort)
	if err != nil {
		return err
	}
	if lines <= 0 {
		lines = 200
	}
	remoteCmd := rascaldJournalctlRemoteCmd(lines, follow)
	return runLocal("ssh", sshArgs(cfg, remoteCmd)...)
}

func rascaldJournalctlRemoteCmd(lines int, follow bool) string {
	if lines <= 0 {
		lines = 200
	}
	return fmt.Sprintf(strings.Join([]string{
		"set -euo pipefail",
		`slot=""`,
		`if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi`,
		`case "$slot" in`,
		`  blue|green) unit="rascal@$slot" ;;`,
		`  *)`,
		`    if systemctl is-active --quiet rascal@green; then`,
		`      unit=rascal@green`,
		`    elif systemctl is-active --quiet rascal@blue; then`,
		`      unit=rascal@blue`,
		`    elif systemctl is-active --quiet rascal; then`,
		`      unit=rascal`,
		`    else`,
		`      unit=rascal@green`,
		`    fi`,
		`    ;;`,
		`esac`,
		"journalctl -u \"$unit\" --no-pager -n %d%s",
	}, "\n"), lines, ternary(follow, " -f", ""))
}

func (a *app) streamRemoteFileLogs(path, host, sshUser, sshKey string, sshPort int, follow bool, lines int) error {
	cfg, err := a.resolveSSHLogConfig(host, sshUser, sshKey, sshPort)
	if err != nil {
		return err
	}
	if lines <= 0 {
		lines = 200
	}
	path = strings.TrimSpace(path)
	if path == "" {
		return &cliError{Code: exitInput, Message: "log path is required"}
	}
	var remoteCmd string
	if follow {
		remoteCmd = fmt.Sprintf("set -euo pipefail; test -f %s; tail -n %d -F %s", shellSingleQuote(path), lines, shellSingleQuote(path))
	} else {
		remoteCmd = fmt.Sprintf("set -euo pipefail; test -f %s; tail -n %d %s", shellSingleQuote(path), lines, shellSingleQuote(path))
	}
	return runLocal("ssh", sshArgs(cfg, remoteCmd)...)
}

func ternary[T any](cond bool, a, b T) T {
	if cond {
		return a
	}
	return b
}

func (a *app) newDoctorCmd() *cobra.Command {
	var (
		fix     bool
		host    string
		sshUser string
		sshKey  string
		sshPort int
	)
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect local config and optional remote server readiness",
		Example: strings.TrimSpace(`
  rascal doctor
  rascal doctor --fix
  rascal doctor --host 203.0.113.10
`),
		RunE: func(_ *cobra.Command, _ []string) error {
			_, statErr := os.Stat(a.configPath)
			cfgExists := statErr == nil

			if fix && !cfgExists {
				if err := config.SaveClientConfig(a.configPath, a.cfg); err != nil {
					return &cliError{Code: exitConfig, Message: "failed to auto-fix config", Cause: err}
				}
				cfgExists = true
			}

			effectiveSSHHost := firstNonEmpty(strings.TrimSpace(host), strings.TrimSpace(a.cfg.SSHHost), strings.TrimSpace(a.cfg.Host))
			resolvedTransport := resolveTransport(a.cfg.Transport, a.cfg.ServerURL, effectiveSSHHost)
			healthOK, healthErr := false, ""
			if resolvedTransport == "ssh" {
				if effectiveSSHHost == "" {
					healthErr = "ssh transport selected but no ssh host is configured"
				} else {
					healthOK, healthErr = checkServerHealthSSH(deployConfig{
						Host:       effectiveSSHHost,
						SSHUser:    firstNonEmpty(strings.TrimSpace(sshUser), strings.TrimSpace(a.cfg.SSHUser), "root"),
						SSHKeyPath: firstNonEmpty(strings.TrimSpace(sshKey), strings.TrimSpace(a.cfg.SSHKey)),
						SSHPort:    firstPositive(sshPort, a.cfg.SSHPort, 22),
					})
				}
			} else {
				healthOK, healthErr = checkServerHealth(a.cfg.ServerURL)
			}
			healthMessage := ""
			if !healthOK {
				healthMessage = healthErr
			}

			var remote map[string]any
			if strings.TrimSpace(host) != "" {
				remoteStatus, err := runRemoteDoctor(deployConfig{
					Host:       strings.TrimSpace(host),
					SSHUser:    firstNonEmpty(strings.TrimSpace(sshUser), "root"),
					SSHKeyPath: strings.TrimSpace(sshKey),
					SSHPort:    sshPort,
				})
				if err != nil {
					remote = map[string]any{
						"host":  strings.TrimSpace(host),
						"error": err.Error(),
					}
				} else {
					remote = map[string]any{
						"host":                 remoteStatus.Host,
						"rascal_service":       remoteStatus.RascalService,
						"active_slot":          remoteStatus.ActiveSlot,
						"docker_installed":     remoteStatus.DockerInstalled,
						"sqlite_installed":     remoteStatus.SQLiteInstalled,
						"caddy_installed":      remoteStatus.CaddyInstalled,
						"env_file_present":     remoteStatus.EnvFilePresent,
						"auth_runtime_synced":  remoteStatus.AuthRuntimeSynced,
						"codex_auth_present":   remoteStatus.CodexAuthPresent,
						"runner_image_present": remoteStatus.RunnerImagePresent,
					}
				}
			}

			diagnostics := map[string]any{
				"config_path":         a.configPath,
				"config_exists":       cfgExists,
				"server_url":          a.cfg.ServerURL,
				"host":                a.cfg.Host,
				"domain":              a.cfg.Domain,
				"transport":           a.cfg.Transport,
				"resolved_transport":  resolvedTransport,
				"ssh_host":            a.cfg.SSHHost,
				"effective_ssh_host":  effectiveSSHHost,
				"ssh_user":            a.cfg.SSHUser,
				"ssh_key":             a.cfg.SSHKey,
				"ssh_port":            a.cfg.SSHPort,
				"server_source":       a.serverSource,
				"api_token_set":       strings.TrimSpace(a.cfg.APIToken) != "",
				"api_token_source":    a.tokenSource,
				"default_repo":        a.cfg.DefaultRepo,
				"default_repo_source": a.repoSource,
				"output_format":       a.output,
				"no_color":            noColorRequested(a.noColor),
				"server_health_ok":    healthOK,
				"server_health_error": healthMessage,
			}
			if remote != nil {
				diagnostics["remote"] = remote
			}
			return a.emit(diagnostics, func() error {
				a.println("local config")
				a.println("config path: %s", a.configPath)
				if cfgExists {
					a.println("config file: present")
				} else {
					a.println("config file: missing")
				}
				a.println("server: %s (%s)", a.cfg.ServerURL, a.serverSource)
				a.println("transport: %s (resolved=%s)", a.cfg.Transport, resolvedTransport)
				if strings.TrimSpace(effectiveSSHHost) != "" {
					a.println("ssh target: %s@%s:%d", firstNonEmpty(strings.TrimSpace(sshUser), strings.TrimSpace(a.cfg.SSHUser), "root"), effectiveSSHHost, firstPositive(sshPort, a.cfg.SSHPort, 22))
				}
				if strings.TrimSpace(a.cfg.Host) != "" {
					a.println("host: %s", a.cfg.Host)
				}
				if strings.TrimSpace(a.cfg.Domain) != "" {
					a.println("domain: %s", a.cfg.Domain)
				}
				if a.cfg.APIToken == "" {
					a.println("local rascal api token: missing")
				} else {
					a.println("local rascal api token: set (%s)", a.tokenSource)
				}
				if a.cfg.DefaultRepo == "" {
					a.println("default repo: not set")
				} else {
					a.println("default repo: %s (%s)", a.cfg.DefaultRepo, a.repoSource)
				}
				if healthOK {
					a.println("server health: ok")
				} else {
					a.println("server health: failed (%s)", healthMessage)
				}
				if remote != nil {
					a.println("remote server")
					if errText, ok := remote["error"].(string); ok && strings.TrimSpace(errText) != "" {
						a.println("remote (%s): error: %s", strings.TrimSpace(host), errText)
					} else {
						activeSlot := strings.TrimSpace(fmt.Sprintf("%v", remote["active_slot"]))
						if activeSlot == "" || activeSlot == "<nil>" {
							activeSlot = "unknown"
						}
						a.println("remote (%s): rascal=%v slot=%v docker=%v sqlite=%v caddy=%v env=%v auth_synced=%v codex_auth=%v runner_image=%v",
							remote["host"], remote["rascal_service"], activeSlot, remote["docker_installed"], remote["sqlite_installed"], remote["caddy_installed"], remote["env_file_present"], remote["auth_runtime_synced"], remote["codex_auth_present"], remote["runner_image_present"])
					}
				}
				if !cfgExists {
					a.println("hint: local config missing; run `rascal init` or rerun `rascal bootstrap`")
				}
				if strings.TrimSpace(a.cfg.DefaultRepo) == "" {
					a.println("hint: set default repo: `rascal config set default_repo OWNER/REPO`")
				}
				if strings.TrimSpace(a.cfg.APIToken) == "" {
					a.println("hint: set local API token: `rascal config set api_token <token>`")
				}
				if remote != nil {
					if synced, ok := remote["auth_runtime_synced"].(bool); ok && !synced {
						activeSlot := strings.TrimSpace(fmt.Sprintf("%v", remote["active_slot"]))
						targetUser := firstNonEmpty(strings.TrimSpace(sshUser), strings.TrimSpace(a.cfg.SSHUser), "root")
						targetHost := firstNonEmpty(strings.TrimSpace(host), strings.TrimSpace(a.cfg.SSHHost), strings.TrimSpace(a.cfg.Host))
						if activeSlot == "legacy" {
							a.println("hint: remote rascal.env changed after service start; restart service: `ssh %s@%s 'systemctl restart rascal'`", targetUser, targetHost)
						} else {
							a.println("hint: remote rascal.env changed after service start; restart active slot: `ssh %s@%s 'slot=$(cat /etc/rascal/active_slot 2>/dev/null || echo blue); systemctl restart rascal@$slot'`", targetUser, targetHost)
						}
					}
				}
				return nil
			})
		},
	}
	cmd.Flags().BoolVar(&fix, "fix", false, "attempt safe auto-fixes (create config file)")
	cmd.Flags().StringVar(&host, "host", "", "optional remote host to validate over SSH")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH target user for remote checks")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path for remote checks")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH target port for remote checks")
	return cmd
}

func (a *app) newOpenCmd() *cobra.Command {
	var printOnly bool
	cmd := &cobra.Command{
		Use:   "open <run_id>",
		Short: "Open PR URL for a run in your browser",
		Long:  "Open the pull request URL produced by a run. Use `--print` for script-friendly output.",
		Example: strings.TrimSpace(`
rascal open run_abc123
rascal open run_abc123 --print
`),
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
			if printOnly {
				a.println(run.PRURL)
				return nil
			}
			if err := openURLInBrowser(run.PRURL); err != nil {
				a.println(run.PRURL)
				return &cliError{Code: exitRuntime, Message: "failed to open browser", Hint: "use --print to only print URL", Cause: err}
			}
			a.println("opened: %s", run.PRURL)
			return nil
		},
	}
	cmd.Flags().BoolVar(&printOnly, "print", false, "print URL instead of opening browser")
	return cmd
}

func (a *app) newRetryCmd() *cobra.Command {
	var debug bool
	cmd := &cobra.Command{
		Use:     "retry <run_id>",
		Aliases: []string{"rerun"},
		Short:   "Create a new run from an existing run",
		Long:    "Retry a failed or canceled run by creating a new run with the same task/repository inputs.",
		Example: strings.TrimSpace(`
rascal retry run_abc123
rascal retry run_abc123 --debug=false
`),
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
				"debug":       debug,
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
	cmd.Flags().BoolVar(&debug, "debug", true, "stream detailed agent execution logs (use --debug=false to reduce verbosity)")
	return cmd
}

func (a *app) newCancelCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "cancel <run_id>",
		Short: "Cancel a queued or running run",
		Long:  "Request cancellation for a queued or running run.",
		Example: strings.TrimSpace(`
rascal cancel run_abc123
`),
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
		Long:  "Show task status, repository, PR number, and pending-input state.",
		Example: strings.TrimSpace(`
rascal task run_abc123
`),
		Args: cobra.ExactArgs(1),
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
	keys := configKeysHelpText()
	cmd := &cobra.Command{
		Use:   "config",
		Short: "Inspect effective config and manage local file values",
		Long: fmt.Sprintf(strings.TrimSpace(`
Inspect effective config and manage values in the local config file.

Supported keys:
%s
`), keys),
		Example: strings.TrimSpace(`
rascal config view
rascal config get default_repo
rascal config set default_repo OWNER/REPO
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "view",
		Short: "View effective config (flags/env/file)",
		Long:  "Show effective config values and where key values were sourced from (flag/env/config/default).",
		Example: strings.TrimSpace(`
rascal config view
rascal config view --output json
`),
		RunE: func(_ *cobra.Command, _ []string) error {
			view := map[string]any{
				"config_path":         a.configPath,
				"server_url":          a.cfg.ServerURL,
				"api_token":           maskSecret(a.cfg.APIToken),
				"default_repo":        a.cfg.DefaultRepo,
				"host":                a.cfg.Host,
				"domain":              a.cfg.Domain,
				"transport":           a.cfg.Transport,
				"ssh_host":            a.cfg.SSHHost,
				"ssh_user":            a.cfg.SSHUser,
				"ssh_key":             a.cfg.SSHKey,
				"ssh_port":            a.cfg.SSHPort,
				"server_source":       a.serverSource,
				"api_token_source":    a.tokenSource,
				"default_repo_source": a.repoSource,
				"transport_source":    a.transportSource,
				"resolved_transport":  a.client.transport,
			}
			return a.emit(view, func() error {
				for _, key := range []string{"config_path", "server_url", "api_token", "default_repo", "host", "domain", "transport", "ssh_host", "ssh_user", "ssh_key", "ssh_port", "server_source", "api_token_source", "default_repo_source", "transport_source", "resolved_transport"} {
					a.println("%s: %v", key, view[key])
				}
				return nil
			})
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "get <key>",
		Short: "Get a config key from the local config file",
		Long: fmt.Sprintf(strings.TrimSpace(`
Get one value from the local config file.

Supported keys:
%s
`), keys),
		Example: strings.TrimSpace(`
rascal config get server_url
rascal config get default_repo
`),
		Args: cobra.ExactArgs(1),
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
			case "host":
				a.println(cfg.Host)
			case "domain":
				a.println(cfg.Domain)
			case "transport":
				a.println(cfg.Transport)
			case "ssh_host":
				a.println(cfg.SSHHost)
			case "ssh_user":
				a.println(cfg.SSHUser)
			case "ssh_key":
				a.println(cfg.SSHKey)
			case "ssh_port":
				a.println("%d", cfg.SSHPort)
			default:
				return &cliError{Code: exitInput, Message: "invalid key", Hint: configKeysHint()}
			}
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "set <key> <value>",
		Short: "Set a config key in the local config file",
		Long: fmt.Sprintf(strings.TrimSpace(`
Set one value in the local config file.

Supported keys:
%s
`), keys),
		Example: strings.TrimSpace(`
rascal config set default_repo OWNER/REPO
rascal config set transport ssh
`),
		Args: cobra.ExactArgs(2),
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
			case "host":
				cfg.Host = val
			case "domain":
				cfg.Domain = val
			case "transport":
				transport := strings.ToLower(val)
				switch transport {
				case "auto", "http", "ssh":
					cfg.Transport = transport
				default:
					return &cliError{Code: exitInput, Message: "invalid transport", Hint: "transport must be one of: auto|http|ssh"}
				}
			case "ssh_host":
				cfg.SSHHost = val
			case "ssh_user":
				cfg.SSHUser = val
			case "ssh_key":
				cfg.SSHKey = val
			case "ssh_port":
				var port int
				if _, err := fmt.Sscanf(val, "%d", &port); err != nil || port <= 0 {
					return &cliError{Code: exitInput, Message: "invalid ssh_port", Hint: "ssh_port must be a positive integer"}
				}
				cfg.SSHPort = port
			default:
				return &cliError{Code: exitInput, Message: "invalid key", Hint: configKeysHint()}
			}
			if err := config.SaveClientConfig(a.configPath, cfg); err != nil {
				return &cliError{Code: exitConfig, Message: "failed to write config", Cause: err}
			}
			a.println("updated %s in %s", key, a.configPath)
			return nil
		},
	})
	cmd.AddCommand(&cobra.Command{
		Use:   "unset <key>",
		Short: "Unset a config key in the local config file",
		Long: fmt.Sprintf(strings.TrimSpace(`
Remove one key from the local config file.

Supported keys:
%s
`), keys),
		Example: strings.TrimSpace(`
rascal config unset server_url
rascal config unset default_repo
`),
		Args: cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			key := strings.TrimSpace(args[0])
			if !isSupportedConfigKey(key) {
				return &cliError{Code: exitInput, Message: "invalid key", Hint: configKeysHint()}
			}
			settings, exists, err := loadFileConfigMap(a.configPath)
			if err != nil {
				return err
			}
			_, hadKey := settings[key]
			status := "absent"
			message := ""
			if hadKey {
				delete(settings, key)
				if err := saveClientConfigMap(a.configPath, settings); err != nil {
					return err
				}
				status = "removed"
				message = fmt.Sprintf("removed %s from %s", key, a.configPath)
			} else if exists {
				message = fmt.Sprintf("%s already absent in %s", key, a.configPath)
			} else {
				message = fmt.Sprintf("%s already absent (config file missing)", key)
			}
			if err := a.initConfig(); err != nil {
				return err
			}
			value, source := a.effectiveConfigValue(key, settings)
			out := map[string]any{
				"key":         key,
				"value":       value,
				"source":      source,
				"status":      status,
				"message":     message,
				"config_path": a.configPath,
			}
			return a.emit(out, func() error {
				if message != "" {
					a.println(message)
				}
				a.println("effective %s: %v (%s)", key, value, source)
				return nil
			})
		},
	})
	return cmd
}

func (a *app) newAuthCmd() *cobra.Command {
	var (
		writeConfig        bool
		showRaw            bool
		host               string
		sshUser            string
		sshKey             string
		sshPort            int
		githubRuntimeToken string
		restartSvc         bool
	)
	cmd := &cobra.Command{
		Use:   "auth",
		Short: "Authentication helpers",
		Long:  "Manage local and remote authentication secrets used by rascald and the CLI.",
		Example: strings.TrimSpace(`
rascal auth rotate --write-config
rascal auth sync --host 203.0.113.10
`),
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(&cobra.Command{
		Use:   "rotate",
		Short: "Generate fresh API/webhook tokens and optionally sync remote server",
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
			if strings.TrimSpace(host) != "" {
				githubRuntimeToken = firstNonEmpty(strings.TrimSpace(githubRuntimeToken), strings.TrimSpace(os.Getenv("GITHUB_RUNTIME_TOKEN")), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_RUNTIME_TOKEN")))
				if githubRuntimeToken == "" {
					return &cliError{Code: exitInput, Message: "--github-runtime-token is required when --host is set"}
				}
				if err := syncRemoteAuth(syncRemoteAuthConfig{
					Host:          strings.TrimSpace(host),
					SSHUser:       firstNonEmpty(strings.TrimSpace(sshUser), "root"),
					SSHKeyPath:    strings.TrimSpace(sshKey),
					SSHPort:       sshPort,
					APIToken:      apiToken,
					GitHubRuntime: githubRuntimeToken,
					WebhookSecret: webhookSecret,
					Restart:       restartSvc,
				}); err != nil {
					return &cliError{Code: exitRuntime, Message: "failed to sync remote auth", Cause: err}
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
				"synced_remote":  strings.TrimSpace(host) != "",
			}
			return a.emit(out, func() error {
				a.println("api_token: %s", displayAPI)
				a.println("webhook_secret: %s", displayWebhook)
				if strings.TrimSpace(host) != "" {
					a.println("synced remote auth on host: %s", strings.TrimSpace(host))
				}
				if !showRaw {
					a.println("use --show to print raw values")
				}
				return nil
			})
		},
	})
	cmd.AddCommand(a.newAuthSyncCmd())
	cmd.PersistentFlags().BoolVar(&writeConfig, "write-config", false, "write generated API token to local config")
	cmd.PersistentFlags().BoolVar(&showRaw, "show", false, "print raw token values")
	cmd.PersistentFlags().StringVar(&host, "host", "", "existing server host for remote auth sync")
	cmd.PersistentFlags().StringVar(&sshUser, "ssh-user", "root", "SSH target user for remote auth sync")
	cmd.PersistentFlags().StringVar(&sshKey, "ssh-key", "", "SSH private key path for remote auth sync")
	cmd.PersistentFlags().IntVar(&sshPort, "ssh-port", 22, "SSH target port for remote auth sync")
	cmd.PersistentFlags().StringVar(&githubRuntimeToken, "github-runtime-token", "", "GitHub runtime token for remote auth sync")
	cmd.PersistentFlags().BoolVar(&restartSvc, "restart-service", true, "restart active rascal slot after remote auth sync")
	return cmd
}

func (a *app) newAuthSyncCmd() *cobra.Command {
	var (
		host               string
		sshUser            string
		sshKey             string
		sshPort            int
		apiToken           string
		githubRuntimeToken string
		webhookSecret      string
		codexAuthPath      string
		restartSvc         bool
	)
	cmd := &cobra.Command{
		Use:   "sync",
		Short: "Sync auth material to remote host over SSH",
		RunE: func(_ *cobra.Command, _ []string) error {
			host = strings.TrimSpace(host)
			if host == "" {
				return &cliError{Code: exitInput, Message: "--host is required"}
			}
			if sshPort <= 0 {
				return &cliError{Code: exitInput, Message: "--ssh-port must be positive"}
			}
			apiToken = strings.TrimSpace(apiToken)
			githubRuntimeToken = strings.TrimSpace(githubRuntimeToken)
			webhookSecret = strings.TrimSpace(webhookSecret)
			codexAuthPath = strings.TrimSpace(codexAuthPath)

			syncCodexAuth := codexAuthPath != ""
			syncServerAuth := !syncCodexAuth || apiToken != "" || githubRuntimeToken != "" || webhookSecret != ""
			if syncServerAuth {
				apiToken = firstNonEmpty(apiToken, strings.TrimSpace(a.cfg.APIToken))
				if apiToken == "" {
					return &cliError{Code: exitInput, Message: "missing API token", Hint: "pass --api-token or set local config"}
				}
				githubRuntimeToken = firstNonEmpty(githubRuntimeToken, strings.TrimSpace(os.Getenv("GITHUB_RUNTIME_TOKEN")), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_RUNTIME_TOKEN")))
				if githubRuntimeToken == "" {
					return &cliError{Code: exitInput, Message: "missing GitHub runtime token", Hint: "pass --github-runtime-token or set GITHUB_RUNTIME_TOKEN"}
				}
				if webhookSecret == "" {
					return &cliError{Code: exitInput, Message: "missing webhook secret", Hint: "pass --webhook-secret"}
				}
			}
			if err := syncRemoteAuth(syncRemoteAuthConfig{
				Host:          host,
				SSHUser:       firstNonEmpty(strings.TrimSpace(sshUser), "root"),
				SSHKeyPath:    strings.TrimSpace(sshKey),
				SSHPort:       sshPort,
				APIToken:      apiToken,
				GitHubRuntime: githubRuntimeToken,
				WebhookSecret: webhookSecret,
				CodexAuthPath: codexAuthPath,
				Restart:       restartSvc,
			}); err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to sync auth", Cause: err}
			}
			return a.emit(map[string]any{
				"host":             host,
				"synced_env_auth":  syncServerAuth,
				"synced_codex":     syncCodexAuth,
				"api_token":        maskSecret(apiToken),
				"webhook_secret":   maskSecret(webhookSecret),
				"codex_auth_path":  codexAuthPath,
				"restarted_service": restartSvc,
			}, func() error {
				if syncServerAuth && syncCodexAuth {
					a.println("synced server env auth and codex auth on %s", host)
				} else if syncCodexAuth {
					a.println("synced codex auth on %s", host)
				} else {
					a.println("synced server env auth on %s", host)
				}
				if restartSvc {
					a.println("active rascal slot restarted")
				}
				return nil
			})
		},
	}
	cmd.Flags().StringVar(&host, "host", "", "existing server host")
	cmd.Flags().StringVar(&sshUser, "ssh-user", "root", "SSH target user")
	cmd.Flags().StringVar(&sshKey, "ssh-key", "", "SSH private key path")
	cmd.Flags().IntVar(&sshPort, "ssh-port", 22, "SSH target port")
	cmd.Flags().StringVar(&apiToken, "api-token", "", "orchestrator API token (defaults to current config)")
	cmd.Flags().StringVar(&githubRuntimeToken, "github-runtime-token", "", "GitHub runtime token (or GITHUB_RUNTIME_TOKEN)")
	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret")
	cmd.Flags().StringVar(&codexAuthPath, "codex-auth", "", "local Codex auth.json path to upload to /etc/rascal/codex_auth.json")
	cmd.Flags().BoolVar(&restartSvc, "restart-service", true, "restart active rascal slot after updating env")
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
	runs, err := a.fetchRuns(100, false)
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

func (a *app) fetchRuns(limit int, all bool) ([]state.Run, error) {
	if limit <= 0 {
		limit = 10
	}
	path := "/v1/runs"
	if all {
		path += "?all=1"
	} else {
		path += fmt.Sprintf("?limit=%d", limit)
	}
	resp, err := a.client.do(http.MethodGet, path, nil)
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

func psStatusLabel(run state.Run) string {
	switch run.Status {
	case state.StatusReview:
		return "review"
	default:
		return string(run.Status)
	}
}

func psPRLabel(run state.Run) string {
	prStatus := effectivePRStatus(run)
	if run.PRNumber <= 0 {
		if strings.TrimSpace(run.PRURL) == "" {
			return "-"
		}
		if prStatus == state.PRStatusOpen {
			return "open"
		}
		return "link"
	}
	switch prStatus {
	case state.PRStatusOpen:
		return fmt.Sprintf("#%d open", run.PRNumber)
	case state.PRStatusMerged:
		return fmt.Sprintf("#%d merged", run.PRNumber)
	case state.PRStatusClosedUnmerged:
		return fmt.Sprintf("#%d closed", run.PRNumber)
	default:
		return fmt.Sprintf("#%d", run.PRNumber)
	}
}

func psCreatedLabel(createdAt time.Time) string {
	return createdAt.UTC().Format("2006-01-02 15:04")
}

func effectivePRStatus(run state.Run) state.PRStatus {
	switch run.PRStatus {
	case state.PRStatusOpen, state.PRStatusMerged, state.PRStatusClosedUnmerged:
		return run.PRStatus
	}
	if run.PRNumber <= 0 && strings.TrimSpace(run.PRURL) == "" {
		return state.PRStatusNone
	}
	switch run.Status {
	case state.StatusReview:
		return state.PRStatusOpen
	case state.StatusSucceeded:
		return state.PRStatusMerged
	case state.StatusCanceled:
		return state.PRStatusClosedUnmerged
	default:
		return state.PRStatusOpen
	}
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
	v.SetConfigType("toml")
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
		Host:        strings.TrimSpace(v.GetString("host")),
		Domain:      strings.TrimSpace(v.GetString("domain")),
		Transport:   strings.TrimSpace(v.GetString("transport")),
		SSHHost:     strings.TrimSpace(v.GetString("ssh_host")),
		SSHUser:     strings.TrimSpace(v.GetString("ssh_user")),
		SSHKey:      strings.TrimSpace(v.GetString("ssh_key")),
		SSHPort:     v.GetInt("ssh_port"),
	}, nil
}

func loadFileConfigMap(path string) (map[string]any, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, false, nil
		}
		return nil, false, &cliError{Code: exitConfig, Message: "failed to read config file", Cause: err}
	}
	if len(bytes.TrimSpace(data)) == 0 {
		return map[string]any{}, true, nil
	}
	var out map[string]any
	if err := toml.Unmarshal(data, &out); err != nil {
		return nil, true, &cliError{Code: exitConfig, Message: "failed to parse config file", Cause: err}
	}
	if out == nil {
		out = map[string]any{}
	}
	return out, true, nil
}

func saveClientConfigMap(path string, settings map[string]any) error {
	if path == "" {
		path = config.DefaultClientConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return &cliError{Code: exitConfig, Message: "failed to create config directory", Cause: err}
	}
	data, err := toml.Marshal(settings)
	if err != nil {
		return &cliError{Code: exitConfig, Message: "failed to serialize config file", Cause: err}
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return &cliError{Code: exitConfig, Message: "failed to write config file", Cause: err}
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return &cliError{Code: exitConfig, Message: "failed to chmod config file", Cause: err}
	}
	return nil
}

func loadEnvFile(path string) (map[string]string, error) {
	out := map[string]string{}
	path = strings.TrimSpace(path)
	if path == "" {
		return out, nil
	}
	expanded, err := expandPath(path)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(expanded)
	if err != nil {
		return nil, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		idx := strings.IndexRune(line, '=')
		if idx <= 0 {
			return nil, fmt.Errorf("invalid line %d: expected KEY=VALUE", lineNo)
		}
		key := strings.TrimSpace(line[:idx])
		value := strings.TrimSpace(line[idx+1:])
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') || (value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if key == "" {
			return nil, fmt.Errorf("invalid line %d: empty key", lineNo)
		}
		out[key] = value
	}
	if err := scanner.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func (a *app) loadGlobalEnv() error {
	explicitPath := strings.TrimSpace(a.envFilePath)
	if explicitPath == "" {
		explicitPath = strings.TrimSpace(os.Getenv("RASCAL_ENV_FILE"))
	}
	path := ""
	if explicitPath != "" {
		expanded, err := expandPath(explicitPath)
		if err != nil {
			return err
		}
		st, err := os.Stat(expanded)
		if err != nil {
			return err
		}
		if st.IsDir() {
			return fmt.Errorf("env file path is a directory: %s", expanded)
		}
		path = expanded
	} else {
		cwd, err := os.Getwd()
		if err != nil {
			return err
		}
		candidate := filepath.Join(cwd, ".rascal.env")
		st, err := os.Stat(candidate)
		if err != nil {
			if !os.IsNotExist(err) {
				return err
			}
		} else if !st.IsDir() {
			path = candidate
		}
	}
	if path == "" {
		return nil
	}
	envMap, err := loadEnvFile(path)
	if err != nil {
		return err
	}
	for k, v := range envMap {
		if strings.TrimSpace(k) == "" {
			continue
		}
		if existing, ok := os.LookupEnv(k); ok && strings.TrimSpace(existing) != "" {
			continue
		}
		if err := os.Setenv(k, v); err != nil {
			return fmt.Errorf("set %s from env file: %w", k, err)
		}
	}
	return nil
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
	if strings.EqualFold(c.transport, "ssh") {
		return c.doOverSSH(method, path, body)
	}
	return c.doOverHTTP(method, path, body)
}

func (c apiClient) doOverHTTP(method, path string, body io.Reader) (*http.Response, error) {
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

func (c apiClient) doOverSSH(method, path string, body io.Reader) (*http.Response, error) {
	sshHost := strings.TrimSpace(c.sshHost)
	if sshHost == "" {
		return nil, fmt.Errorf("ssh transport selected but ssh host is missing")
	}
	sshUser := firstNonEmpty(strings.TrimSpace(c.sshUser), "root")
	sshPort := c.sshPort
	if sshPort <= 0 {
		sshPort = 22
	}
	sshKey := strings.TrimSpace(c.sshKey)

	var payload []byte
	if body != nil {
		data, err := io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("read request body: %w", err)
		}
		payload = data
	}

	curlArgs := []string{
		"curl", "-sS", "-i", "--raw",
		"-X", shellSingleQuote(strings.TrimSpace(method)),
		"-H", shellSingleQuote("Accept: application/json"),
	}
	if c.token != "" {
		curlArgs = append(curlArgs, "-H", shellSingleQuote("Authorization: Bearer "+c.token))
	}
	if len(payload) > 0 {
		curlArgs = append(curlArgs, "-H", shellSingleQuote("Content-Type: application/json"), "--data-binary", "@-")
	}
	curlCmd := strings.Join(curlArgs, " ")
	remoteCmd := strings.Join([]string{
		"set -eu",
		"slot=''",
		"if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi",
		"case \"$slot\" in",
		fmt.Sprintf("  %s) port=%d ;;", rascalSlotBlue, rascalSlotBluePort),
		fmt.Sprintf("  %s) port=%d ;;", rascalSlotGreen, rascalSlotGreenPort),
		fmt.Sprintf("  *) if systemctl is-active --quiet rascal; then port=%d; else port=%d; fi ;;", rascalProxyPort, rascalSlotBluePort),
		"esac",
		"url=$(printf 'http://127.0.0.1:%s%s' \"$port\" " + shellSingleQuote(path) + ")",
		curlCmd + " \"$url\"",
	}, "\n")

	cfg := deployConfig{
		Host:       sshHost,
		SSHUser:    sshUser,
		SSHKeyPath: sshKey,
		SSHPort:    sshPort,
	}
	cmd := exec.Command("ssh", sshArgs(cfg, remoteCmd)...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if len(payload) > 0 {
		cmd.Stdin = bytes.NewReader(payload)
	}
	if err := cmd.Run(); err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut == "" {
			errOut = strings.TrimSpace(stdout.String())
		}
		if errOut == "" {
			return nil, fmt.Errorf("ssh request failed: %w", err)
		}
		return nil, fmt.Errorf("ssh request failed: %w (%s)", err, errOut)
	}

	raw := stdout.Bytes()
	resp, err := parseRawHTTPResponse(raw, method)
	if err != nil {
		errOut := strings.TrimSpace(stderr.String())
		if errOut != "" {
			return nil, fmt.Errorf("parse ssh response: %w (%s)", err, errOut)
		}
		return nil, fmt.Errorf("parse ssh response: %w", err)
	}
	return resp, nil
}

func parseRawHTTPResponse(raw []byte, method string) (*http.Response, error) {
	reader := bufio.NewReader(bytes.NewReader(raw))
	req := &http.Request{Method: method}
	resp, err := http.ReadResponse(reader, req)
	if err != nil {
		return nil, err
	}
	return resp, nil
}

func shellSingleQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
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

type deployConfig = deployengine.Config

const (
	rascalSlotBlue      = deployengine.SlotBlue
	rascalSlotGreen     = deployengine.SlotGreen
	rascalSlotBluePort  = deployengine.SlotBluePort
	rascalSlotGreenPort = deployengine.SlotGreenPort
	rascalProxyPort     = deployengine.ProxyPort
)

func deployToExistingHost(cfg deployConfig) error {
	return deployengine.Execute(cfg)
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

func runLocalCapture(name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		if text == "" {
			return "", fmt.Errorf("%s failed: %w", name, err)
		}
		return text, fmt.Errorf("%s failed: %w (%s)", name, err, text)
	}
	return text, nil
}

func detectRemoteGOARCH(cfg deployConfig) (string, error) {
	return deployengine.DetectRemoteGOARCH(cfg)
}

func goarchFromUnameMachine(machine string) (string, bool) {
	return deployengine.GoarchFromUnameMachine(machine)
}

func goarchFromHetznerArchitecture(arch string) (string, bool) {
	return deployengine.GoarchFromHetznerArchitecture(arch)
}

func sshArgs(cfg deployConfig, remoteCmd string) []string {
	args := []string{"-p", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if keyPath := normalizedSSHKeyPath(cfg.SSHKeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, fmt.Sprintf("%s@%s", cfg.SSHUser, cfg.Host), remoteCmd)
	return args
}

func scpArgs(cfg deployConfig, source, target string) []string {
	args := []string{"-P", fmt.Sprintf("%d", cfg.SSHPort), "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new"}
	if keyPath := normalizedSSHKeyPath(cfg.SSHKeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, source, target)
	return args
}

func normalizedSSHKeyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	expanded, err := expandPath(path)
	if err != nil {
		return path
	}
	return expanded
}

func remoteTarget(cfg deployConfig, path string) string {
	return fmt.Sprintf("%s@%s:%s", cfg.SSHUser, cfg.Host, path)
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

func openURLInBrowser(rawURL string) error {
	rawURL = strings.TrimSpace(rawURL)
	if rawURL == "" {
		return fmt.Errorf("url is empty")
	}
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "darwin":
		cmd = exec.Command("open", rawURL)
	case "windows":
		cmd = exec.Command("rundll32", "url.dll,FileProtocolHandler", rawURL)
	default:
		cmd = exec.Command("xdg-open", rawURL)
	}
	return cmd.Run()
}

func resolveRepo(explicit, defaultRepo string, infer func() string) (string, bool) {
	explicit = strings.TrimSpace(explicit)
	if explicit != "" {
		return explicit, false
	}
	defaultRepo = strings.TrimSpace(defaultRepo)
	if defaultRepo != "" {
		return defaultRepo, false
	}
	if infer == nil {
		return "", false
	}
	inferred := strings.TrimSpace(infer())
	if inferred == "" {
		return "", false
	}
	return inferred, true
}

func inferRepoFromGitRemote() string {
	remote, err := gitRemoteOrigin()
	if err != nil {
		return ""
	}
	repo, ok := parseGitHubRepoFromRemote(remote)
	if !ok {
		return ""
	}
	return repo
}

func gitRemoteOrigin() (string, error) {
	cmd := exec.Command("git", "remote", "get-url", "origin")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func parseGitHubRepoFromRemote(remote string) (string, bool) {
	remote = strings.TrimSpace(remote)
	if remote == "" {
		return "", false
	}
	var path string
	switch {
	case strings.HasPrefix(remote, "git@github.com:"):
		path = strings.TrimPrefix(remote, "git@github.com:")
	case strings.HasPrefix(remote, "https://github.com/"):
		path = strings.TrimPrefix(remote, "https://github.com/")
	case strings.HasPrefix(remote, "http://github.com/"):
		path = strings.TrimPrefix(remote, "http://github.com/")
	default:
		return "", false
	}
	path = strings.TrimSuffix(path, ".git")
	path = strings.TrimSuffix(path, "/")
	parts := strings.Split(path, "/")
	if len(parts) != 2 {
		return "", false
	}
	if strings.TrimSpace(parts[0]) == "" || strings.TrimSpace(parts[1]) == "" {
		return "", false
	}
	return parts[0] + "/" + parts[1], true
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func resolveBootstrapServerURL(serverURL, domain, host, configServerURL string, willProvision bool) (string, string) {
	switch {
	case strings.TrimSpace(serverURL) != "":
		return strings.TrimRight(strings.TrimSpace(serverURL), "/"), "flag"
	case strings.TrimSpace(domain) != "":
		return "https://" + strings.TrimSpace(domain), "domain"
	case strings.TrimSpace(host) != "":
		return "http://" + strings.TrimSpace(host) + ":8080", "host"
	case willProvision:
		return "http://<provisioned-host>:8080", "provisioned_host"
	case strings.TrimSpace(configServerURL) != "" && strings.TrimSpace(configServerURL) != "http://127.0.0.1:8080":
		return strings.TrimRight(strings.TrimSpace(configServerURL), "/"), "config"
	default:
		return "", "unresolved"
	}
}

func firstPositive(values ...int) int {
	for _, v := range values {
		if v > 0 {
			return v
		}
	}
	return 0
}

func supportedConfigKeys() []string {
	return []string{
		"server_url",
		"api_token",
		"default_repo",
		"host",
		"domain",
		"transport",
		"ssh_host",
		"ssh_user",
		"ssh_key",
		"ssh_port",
	}
}

func isSupportedConfigKey(key string) bool {
	key = strings.TrimSpace(key)
	if key == "" {
		return false
	}
	for _, supported := range supportedConfigKeys() {
		if key == supported {
			return true
		}
	}
	return false
}

func configKeysHint() string {
	return "use " + strings.Join(supportedConfigKeys(), "|")
}

func configKeysHelpText() string {
	return strings.Join(supportedConfigKeys(), "\n")
}

func configSourceFromEnvConfig(envKey, configKey string, settings map[string]any, fallback string) string {
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return "env"
	}
	if _, ok := settings[configKey]; ok {
		return "config"
	}
	return fallback
}

func (a *app) configSourceFromFlagEnvConfig(flagSet bool, envKey, configKey string, settings map[string]any, fallback string) string {
	if flagSet {
		return "flag"
	}
	return configSourceFromEnvConfig(envKey, configKey, settings, fallback)
}

func (a *app) sshHostSource(settings map[string]any) string {
	if strings.TrimSpace(a.sshHostFlag) != "" {
		return "flag"
	}
	if strings.TrimSpace(os.Getenv("RASCAL_SSH_HOST")) != "" {
		return "env"
	}
	if _, ok := settings["ssh_host"]; ok {
		return "config"
	}
	if strings.TrimSpace(a.cfg.Host) != "" {
		return "derived"
	}
	return "unset"
}

func (a *app) effectiveConfigValue(key string, settings map[string]any) (any, string) {
	switch key {
	case "server_url":
		return a.cfg.ServerURL, a.serverSource
	case "api_token":
		return maskSecret(a.cfg.APIToken), a.tokenSource
	case "default_repo":
		return a.cfg.DefaultRepo, a.repoSource
	case "host":
		return a.cfg.Host, configSourceFromEnvConfig("RASCAL_HOST", "host", settings, "unset")
	case "domain":
		return a.cfg.Domain, configSourceFromEnvConfig("RASCAL_DOMAIN", "domain", settings, "unset")
	case "transport":
		return a.cfg.Transport, a.transportSource
	case "ssh_host":
		return a.cfg.SSHHost, a.sshHostSource(settings)
	case "ssh_user":
		return a.cfg.SSHUser, a.configSourceFromFlagEnvConfig(strings.TrimSpace(a.sshUserFlag) != "", "RASCAL_SSH_USER", "ssh_user", settings, "default")
	case "ssh_key":
		return a.cfg.SSHKey, a.configSourceFromFlagEnvConfig(strings.TrimSpace(a.sshKeyFlag) != "", "RASCAL_SSH_KEY", "ssh_key", settings, "unset")
	case "ssh_port":
		return a.cfg.SSHPort, a.configSourceFromFlagEnvConfig(a.sshPortFlag > 0, "RASCAL_SSH_PORT", "ssh_port", settings, "default")
	default:
		return "", "unset"
	}
}

func resolveTransport(configured, serverURL, sshHost string) string {
	mode := strings.ToLower(strings.TrimSpace(configured))
	switch mode {
	case "http", "ssh":
		return mode
	}
	if strings.TrimSpace(sshHost) == "" {
		return "http"
	}
	u, err := url.Parse(strings.TrimSpace(serverURL))
	if err != nil {
		return "ssh"
	}
	host := strings.ToLower(strings.TrimSpace(u.Hostname()))
	port := strings.TrimSpace(u.Port())
	if host == "" || host == "127.0.0.1" || host == "localhost" {
		return "ssh"
	}
	if strings.EqualFold(u.Scheme, "http") && port == "8080" {
		return "ssh"
	}
	return "http"
}

func validateDistinctGitHubTokens(adminToken, runtimeToken string, enforce bool) error {
	if !enforce {
		return nil
	}
	if strings.TrimSpace(adminToken) == "" || strings.TrimSpace(runtimeToken) == "" {
		return nil
	}
	if strings.TrimSpace(adminToken) == strings.TrimSpace(runtimeToken) {
		return fmt.Errorf("strict token separation required: --github-admin-token and --github-runtime-token must differ")
	}
	return nil
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
