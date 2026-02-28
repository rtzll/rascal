package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/christianrotzoll/rascal/internal/config"
	ghapi "github.com/christianrotzoll/rascal/internal/github"
	"github.com/christianrotzoll/rascal/internal/state"
)

type apiClient struct {
	baseURL string
	token   string
	http    *http.Client
}

func main() {
	cfg := config.LoadClientConfig()
	client := apiClient{
		baseURL: cfg.ServerURL,
		token:   cfg.APIToken,
		http:    &http.Client{Timeout: 30 * time.Second},
	}

	if len(os.Args) < 2 {
		usage()
		os.Exit(2)
	}

	cmd := os.Args[1]
	switch cmd {
	case "bootstrap":
		if err := runBootstrap(cfg, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "run":
		if err := runTaskCommand(client, cfg, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "issue":
		if err := runIssueCommand(client, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "ps":
		if err := runPSCommand(client); err != nil {
			exitErr(err)
		}
	case "logs":
		if err := runLogsCommand(client, os.Args[2:]); err != nil {
			exitErr(err)
		}
	case "doctor":
		runDoctor(cfg)
	default:
		usage()
		exitErr(fmt.Errorf("unknown command %q", cmd))
	}
}

func usage() {
	fmt.Fprintln(os.Stderr, `rascal commands:
  bootstrap [--repo OWNER/REPO --domain rascal.example.com --github-token TOKEN]
  run   -R OWNER/REPO -t "task text" [-b main]
  issue OWNER/REPO#123
  ps
  logs  <run_id>
  doctor`)
}

func runBootstrap(cfg config.ClientConfig, args []string) error {
	fs := flag.NewFlagSet("bootstrap", flag.ContinueOnError)
	repo := fs.String("repo", cfg.DefaultRepo, "default repository in OWNER/REPO form")
	domain := fs.String("domain", "", "orchestrator domain (e.g. rascal.example.com)")
	serverURL := fs.String("server-url", "", "orchestrator base URL (overrides --domain)")
	apiToken := fs.String("api-token", "", "API token for orchestrator (auto-generated if empty)")
	githubToken := fs.String("github-token", "", "GitHub token for label/webhook setup")
	webhookSecret := fs.String("webhook-secret", "", "GitHub webhook secret (auto-generated if empty)")
	skipWebhook := fs.Bool("skip-webhook", false, "skip GitHub webhook setup")
	writeConfig := fs.Bool("write-config", true, "write ~/.rascal/config.yaml")
	host := fs.String("host", "", "existing server host (stored for deploy instructions)")
	sshUser := fs.String("ssh-user", "root", "SSH user for existing host deployment")
	sshKey := fs.String("ssh-key", "", "SSH private key path for existing host deployment")
	sshPort := fs.Int("ssh-port", 22, "SSH port for existing host deployment")
	goarch := fs.String("goarch", "amd64", "GOARCH for rascald binary when deploying to existing host")
	deployExisting := fs.Bool("deploy-existing", false, "deploy rascald to --host over SSH")
	codexAuthPath := fs.String("codex-auth", "~/.codex/auth.json", "local Codex auth.json path copied to the server")
	hcloudToken := fs.String("hcloud-token", "", "Hetzner Cloud token (provisioning path placeholder)")
	if err := fs.Parse(args); err != nil {
		return err
	}

	*repo = strings.TrimSpace(*repo)
	*domain = strings.TrimSpace(*domain)
	*serverURL = strings.TrimSpace(*serverURL)
	*apiToken = strings.TrimSpace(*apiToken)
	*githubToken = strings.TrimSpace(*githubToken)
	*webhookSecret = strings.TrimSpace(*webhookSecret)
	*host = strings.TrimSpace(*host)
	*sshUser = strings.TrimSpace(*sshUser)
	*sshKey = strings.TrimSpace(*sshKey)
	*goarch = strings.TrimSpace(*goarch)
	*codexAuthPath = strings.TrimSpace(*codexAuthPath)
	*hcloudToken = strings.TrimSpace(*hcloudToken)

	if *serverURL == "" {
		if *domain == "" {
			return fmt.Errorf("either --server-url or --domain is required")
		}
		*serverURL = "https://" + *domain
	}
	*serverURL = strings.TrimRight(*serverURL, "/")

	if *apiToken == "" {
		tok, err := randomToken(32)
		if err != nil {
			return fmt.Errorf("generate api token: %w", err)
		}
		*apiToken = tok
	}
	if *webhookSecret == "" {
		secret, err := randomToken(32)
		if err != nil {
			return fmt.Errorf("generate webhook secret: %w", err)
		}
		*webhookSecret = secret
	}

	if *writeConfig {
		if err := config.SaveClientConfig(config.DefaultClientConfigPath(), config.ClientConfig{
			ServerURL:   *serverURL,
			APIToken:    *apiToken,
			DefaultRepo: *repo,
		}); err != nil {
			return err
		}
	}

	if !*skipWebhook {
		if *repo == "" {
			return fmt.Errorf("--repo is required when webhook setup is enabled")
		}
		if *githubToken == "" {
			return fmt.Errorf("--github-token is required when webhook setup is enabled")
		}
		gh := ghapi.NewAPIClient(*githubToken)
		ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancel()

		if err := gh.EnsureLabel(ctx, *repo, "rascal", "0e8a16", "Trigger Rascal automation"); err != nil {
			return fmt.Errorf("ensure label: %w", err)
		}
		if err := gh.UpsertWebhook(ctx, *repo, *serverURL+"/v1/webhooks/github", *webhookSecret, nil); err != nil {
			return fmt.Errorf("upsert webhook: %w", err)
		}
	}

	if *deployExisting {
		if *host == "" {
			return fmt.Errorf("--host is required when --deploy-existing is set")
		}
		if *githubToken == "" {
			return fmt.Errorf("--github-token is required when --deploy-existing is set")
		}
		if *sshPort <= 0 {
			return fmt.Errorf("--ssh-port must be positive")
		}
		if *codexAuthPath == "" {
			return fmt.Errorf("--codex-auth must be set")
		}
		expandedAuthPath, err := expandPath(*codexAuthPath)
		if err != nil {
			return fmt.Errorf("expand codex auth path: %w", err)
		}
		deployCfg := deployConfig{
			Host:               *host,
			SSHUser:            *sshUser,
			SSHKeyPath:         *sshKey,
			SSHPort:            *sshPort,
			APIToken:           *apiToken,
			WebhookSecret:      *webhookSecret,
			GitHubToken:        *githubToken,
			CodexAuthPath:      expandedAuthPath,
			RunnerMode:         "docker",
			RunnerImage:        "rascal-runner:latest",
			ServerListenAddr:   ":8080",
			ServerDataDir:      "/var/lib/rascal",
			ServerStatePath:    "/var/lib/rascal/state.json",
			ServerCodexAuthDst: "/etc/rascal/codex_auth.json",
			GOARCH:             *goarch,
		}
		if err := deployToExistingHost(deployCfg); err != nil {
			return err
		}
		fmt.Printf("deployed rascald to %s\n", *host)
	}

	fmt.Printf("bootstrap complete\n")
	fmt.Printf("server_url: %s\n", *serverURL)
	fmt.Printf("api_token: %s\n", *apiToken)
	fmt.Printf("default_repo: %s\n", *repo)
	fmt.Printf("webhook_secret: %s\n", *webhookSecret)

	if *host != "" {
		fmt.Printf("\nnext step (server deploy): configure host %s with these env vars:\n", *host)
		fmt.Printf("RASCAL_API_TOKEN=%s\n", *apiToken)
		fmt.Printf("RASCAL_GITHUB_WEBHOOK_SECRET=%s\n", *webhookSecret)
		if *repo != "" {
			fmt.Printf("RASCAL_DEFAULT_REPO=%s\n", *repo)
		}
	}
	if *hcloudToken != "" {
		fmt.Println("hcloud provisioning path is not automated yet; deploy to an existing host for now.")
	}
	return nil
}

func runTaskCommand(client apiClient, cfg config.ClientConfig, args []string) error {
	fs := flag.NewFlagSet("run", flag.ContinueOnError)
	repo := fs.String("R", cfg.DefaultRepo, "repository in OWNER/REPO form")
	task := fs.String("t", "", "task text")
	base := fs.String("b", "main", "base branch")
	if err := fs.Parse(args); err != nil {
		return err
	}

	*repo = strings.TrimSpace(*repo)
	*task = strings.TrimSpace(*task)
	if *repo == "" || *task == "" {
		return fmt.Errorf("both -R and -t are required")
	}

	payload := map[string]any{
		"repo":        *repo,
		"task":        *task,
		"base_branch": strings.TrimSpace(*base),
	}

	resp, err := client.doJSON(http.MethodPost, "/v1/tasks", payload)
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
}

func runIssueCommand(client apiClient, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rascal issue OWNER/REPO#123")
	}
	repo, issueNumber, err := parseIssueRef(args[0])
	if err != nil {
		return err
	}

	payload := map[string]any{
		"repo":         repo,
		"issue_number": issueNumber,
	}
	resp, err := client.doJSON(http.MethodPost, "/v1/tasks/issue", payload)
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
}

func runPSCommand(client apiClient) error {
	resp, err := client.do(http.MethodGet, "/v1/runs", nil)
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
}

func runLogsCommand(client apiClient, args []string) error {
	if len(args) != 1 {
		return fmt.Errorf("usage: rascal logs <run_id>")
	}

	resp, err := client.do(http.MethodGet, "/v1/runs/"+args[0]+"/logs", nil)
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
}

func runDoctor(cfg config.ClientConfig) {
	fmt.Printf("config path: %s\n", config.DefaultClientConfigPath())
	fmt.Printf("server: %s\n", cfg.ServerURL)
	if cfg.APIToken == "" {
		fmt.Println("api token: missing (set RASCAL_API_TOKEN or bootstrap)")
	} else {
		fmt.Println("api token: set")
	}
	if cfg.DefaultRepo == "" {
		fmt.Println("default repo: not set (set RASCAL_DEFAULT_REPO or bootstrap --repo)")
	} else {
		fmt.Printf("default repo: %s\n", cfg.DefaultRepo)
	}
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

func exitErr(err error) {
	fmt.Fprintln(os.Stderr, "error:", err)
	os.Exit(1)
}
