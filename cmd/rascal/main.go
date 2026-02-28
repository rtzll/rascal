package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/rtzll/rascal/internal/state"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
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
	v               *viper.Viper
	cfg             config.ClientConfig
	client          apiClient
}

func main() {
	if err := newRootCmd().Execute(); err != nil {
		exitErr(err)
	}
}

func newRootCmd() *cobra.Command {
	a := &app{}

	root := &cobra.Command{
		Use:           "rascal",
		Short:         "Rascal CLI",
		SilenceUsage:  true,
		SilenceErrors: true,
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			return a.initConfig()
		},
	}

	root.PersistentFlags().StringVar(&a.configPath, "config", config.DefaultClientConfigPath(), "config file path")
	root.PersistentFlags().StringVar(&a.serverURLFlag, "server-url", "", "orchestrator base URL")
	root.PersistentFlags().StringVar(&a.apiTokenFlag, "api-token", "", "orchestrator API token")
	root.PersistentFlags().StringVar(&a.defaultRepoFlag, "default-repo", "", "default repository in OWNER/REPO form")

	root.AddCommand(a.newBootstrapCmd())
	root.AddCommand(a.newRunCmd())
	root.AddCommand(a.newIssueCmd())
	root.AddCommand(a.newPSCmd())
	root.AddCommand(a.newLogsCmd())
	root.AddCommand(a.newDoctorCmd())
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
			return fmt.Errorf("read config file %q: %w", a.configPath, err)
		}
	}
	a.v = v

	a.cfg = config.ClientConfig{
		ServerURL:   strings.TrimSpace(v.GetString("server_url")),
		APIToken:    strings.TrimSpace(v.GetString("api_token")),
		DefaultRepo: strings.TrimSpace(v.GetString("default_repo")),
	}

	if strings.TrimSpace(a.serverURLFlag) != "" {
		a.cfg.ServerURL = strings.TrimSpace(a.serverURLFlag)
	}
	if strings.TrimSpace(a.apiTokenFlag) != "" {
		a.cfg.APIToken = strings.TrimSpace(a.apiTokenFlag)
	}
	if strings.TrimSpace(a.defaultRepoFlag) != "" {
		a.cfg.DefaultRepo = strings.TrimSpace(a.defaultRepoFlag)
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
	return nil
}

func (a *app) newBootstrapCmd() *cobra.Command {
	var (
		repo           string
		domain         string
		serverURL      string
		apiToken       string
		githubToken    string
		webhookSecret  string
		skipWebhook    bool
		writeConfig    bool
		host           string
		sshUser        string
		sshKey         string
		sshPort        int
		goarch         string
		deployExisting bool
		codexAuthPath  string
		hcloudToken    string
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
			hcloudToken = strings.TrimSpace(hcloudToken)

			if serverURL == "" {
				if domain == "" {
					return fmt.Errorf("either --server-url or --domain is required")
				}
				serverURL = "https://" + domain
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
				fmt.Printf("deployed rascald to %s\n", host)
			}

			fmt.Printf("bootstrap complete\n")
			fmt.Printf("server_url: %s\n", serverURL)
			fmt.Printf("api_token: %s\n", maskSecret(apiToken))
			fmt.Printf("default_repo: %s\n", repo)
			fmt.Printf("webhook_secret: %s\n", maskSecret(webhookSecret))
			if writeConfig {
				fmt.Printf("config_path: %s\n", a.configPath)
			}
			if hcloudToken != "" {
				fmt.Println("hcloud provisioning path is not automated yet; deploy to an existing host for now.")
			}
			return nil
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
	cmd.Flags().StringVar(&hcloudToken, "hcloud-token", "", "Hetzner Cloud token (provisioning placeholder)")

	return cmd
}

func (a *app) newRunCmd() *cobra.Command {
	var repo, task, baseBranch string
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Start an ad-hoc run",
		RunE: func(_ *cobra.Command, _ []string) error {
			repo = firstNonEmpty(strings.TrimSpace(repo), a.cfg.DefaultRepo)
			task = strings.TrimSpace(task)
			baseBranch = firstNonEmpty(strings.TrimSpace(baseBranch), "main")
			if repo == "" || task == "" {
				return fmt.Errorf("both --repo/-R and --task/-t are required")
			}

			payload := map[string]any{"repo": repo, "task": task, "base_branch": baseBranch}
			resp, err := a.client.doJSON(http.MethodPost, "/v1/tasks", payload)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			var out struct {
				Run state.Run `json:"run"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Printf("run created: %s (%s)\n", out.Run.ID, out.Run.Status)
			return nil
		},
	}
	cmd.Flags().StringVarP(&repo, "repo", "R", "", "repository in OWNER/REPO form")
	cmd.Flags().StringVarP(&task, "task", "t", "", "task text")
	cmd.Flags().StringVarP(&baseBranch, "base-branch", "b", "main", "base branch")
	return cmd
}

func (a *app) newIssueCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "issue OWNER/REPO#123",
		Short: "Start a run from an issue",
		Args:  cobra.ExactArgs(1),
		RunE: func(_ *cobra.Command, args []string) error {
			repo, issueNumber, err := parseIssueRef(args[0])
			if err != nil {
				return err
			}
			payload := map[string]any{"repo": repo, "issue_number": issueNumber}
			resp, err := a.client.doJSON(http.MethodPost, "/v1/tasks/issue", payload)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			var out struct {
				Run state.Run `json:"run"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}
			fmt.Printf("issue run created: %s (%s)\n", out.Run.ID, out.Run.Status)
			return nil
		},
	}
	return cmd
}

func (a *app) newPSCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "ps",
		Short: "List recent runs",
		RunE: func(_ *cobra.Command, _ []string) error {
			resp, err := a.client.do(http.MethodGet, "/v1/runs", nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}

			var out struct {
				Runs []state.Run `json:"runs"`
			}
			if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
				return fmt.Errorf("decode response: %w", err)
			}

			tw := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
			fmt.Fprintln(tw, "RUN ID\tSTATUS\tREPO\tTASK ID\tCREATED")
			for _, run := range out.Runs {
				fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\n", run.ID, run.Status, run.Repo, run.TaskID, run.CreatedAt.Format(time.RFC3339))
			}
			return tw.Flush()
		},
	}
	return cmd
}

func (a *app) newLogsCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "logs <run_id>",
		Short: "Fetch logs for a run",
		Args:  cobra.ExactArgs(1),
		ValidArgsFunction: func(_ *cobra.Command, args []string, toComplete string) ([]string, cobra.ShellCompDirective) {
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
			return out, cobra.ShellCompDirectiveNoFileComp
		},
		RunE: func(_ *cobra.Command, args []string) error {
			resp, err := a.client.do(http.MethodGet, "/v1/runs/"+args[0]+"/logs", nil)
			if err != nil {
				return err
			}
			defer resp.Body.Close()
			if resp.StatusCode >= 300 {
				body, _ := io.ReadAll(resp.Body)
				return fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
			}
			_, err = io.Copy(os.Stdout, resp.Body)
			return err
		},
	}
	return cmd
}

func (a *app) newDoctorCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "doctor",
		Short: "Inspect local CLI configuration",
		RunE: func(_ *cobra.Command, _ []string) error {
			fmt.Printf("config path: %s\n", a.configPath)
			fmt.Printf("server: %s\n", a.cfg.ServerURL)
			if a.cfg.APIToken == "" {
				fmt.Println("api token: missing (set RASCAL_API_TOKEN or run bootstrap)")
			} else {
				fmt.Println("api token: set")
			}
			if a.cfg.DefaultRepo == "" {
				fmt.Println("default repo: not set (set RASCAL_DEFAULT_REPO or run bootstrap --repo)")
			} else {
				fmt.Printf("default repo: %s\n", a.cfg.DefaultRepo)
			}
			return nil
		},
	}
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
	return cmd
}

func (a *app) fetchRuns(limit int) ([]state.Run, error) {
	resp, err := a.client.do(http.MethodGet, fmt.Sprintf("/v1/runs?limit=%d", limit), nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("server error (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Runs []state.Run `json:"runs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("decode response: %w", err)
	}
	return out.Runs, nil
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

	if err := runLocal("ssh", sshArgs(cfg, "mkdir -p /opt/rascal /etc/rascal /var/lib/rascal /tmp/rascal-bootstrap")...); err != nil {
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
	if _, err := os.Stat(cfg.CodexAuthPath); err == nil {
		if err := runLocal("scp", scpArgs(cfg, cfg.CodexAuthPath, remoteTarget(cfg, "/tmp/rascal-bootstrap/auth.json"))...); err != nil {
			return err
		}
	}

	remoteInstall := strings.Join([]string{
		"set -euo pipefail",
		"install -m 0755 /tmp/rascal-bootstrap/rascald /opt/rascal/rascald",
		"install -m 0600 /tmp/rascal-bootstrap/rascal.env /etc/rascal/rascal.env",
		"install -m 0644 /tmp/rascal-bootstrap/rascal.service /etc/systemd/system/rascal.service",
		fmt.Sprintf("if [ -f /tmp/rascal-bootstrap/auth.json ]; then install -m 0600 /tmp/rascal-bootstrap/auth.json %s; fi", cfg.ServerCodexAuthDst),
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
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
