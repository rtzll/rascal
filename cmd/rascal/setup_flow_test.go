package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
)

func TestSetupFlowCLISequence(t *testing.T) {
	origDeploy := deployToExistingHostFn
	origHealth := waitForServerHealthSSHFn
	origSeed := seedBootstrapSharedCredentialFn
	origCheckServerHealth := checkServerHealthFn
	origCheckServerHealthSSH := checkServerHealthSSHFn
	origRepoClientFactory := newRepoGitHubClientFn
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
		waitForServerHealthSSHFn = origHealth
		seedBootstrapSharedCredentialFn = origSeed
		checkServerHealthFn = origCheckServerHealth
		checkServerHealthSSHFn = origCheckServerHealthSSH
		newRepoGitHubClientFn = origRepoClientFactory
	})

	for _, key := range []string{
		"RASCAL_API_TOKEN",
		"GITHUB_ADMIN_TOKEN",
		"RASCAL_GITHUB_TOKEN",
		"RASCAL_GITHUB_WEBHOOK_SECRET",
		"HCLOUD_TOKEN",
	} {
		t.Setenv(key, "")
	}

	tmp := t.TempDir()
	configPath := filepath.Join(tmp, "config.toml")
	authPath := filepath.Join(tmp, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"access_token":"test"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	loadCfg := func() config.ClientConfig {
		t.Helper()
		cfg, err := config.LoadClientConfigAtPath(configPath)
		if err != nil {
			t.Fatalf("load config: %v", err)
		}
		return cfg
	}

	initApp := &app{configPath: configPath, output: "json"}
	initCmd := initApp.newInitCmd()
	initCmd.SetOut(io.Discard)
	initCmd.SetErr(io.Discard)
	initCmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--host", "203.0.113.10",
		"--domain", "rascal.example.com",
		"--api-token", "api-token",
		"--transport", "ssh",
		"--skip-deploy",
		"--skip-github",
	})
	initStdout, err := captureStdout(func() error { return initCmd.Execute() })
	if err != nil {
		t.Fatalf("init execute: %v", err)
	}

	var initOut initCompleteOutput
	if err := json.Unmarshal([]byte(initStdout), &initOut); err != nil {
		t.Fatalf("decode init output: %v\noutput:\n%s", err, initStdout)
	}
	if initOut.Status != "init_complete" {
		t.Fatalf("init status = %q, want init_complete", initOut.Status)
	}
	if initOut.ServerURL != "https://rascal.example.com" {
		t.Fatalf("init server_url = %q, want https://rascal.example.com", initOut.ServerURL)
	}
	if initOut.DefaultRepo != "owner/repo" {
		t.Fatalf("init default_repo = %q, want owner/repo", initOut.DefaultRepo)
	}
	if initOut.Host != "203.0.113.10" {
		t.Fatalf("init host = %q, want 203.0.113.10", initOut.Host)
	}

	cfg := loadCfg()
	if cfg.ServerURL != "https://rascal.example.com" {
		t.Fatalf("config server_url = %q, want https://rascal.example.com", cfg.ServerURL)
	}
	if cfg.DefaultRepo != "owner/repo" {
		t.Fatalf("config default_repo = %q, want owner/repo", cfg.DefaultRepo)
	}
	if cfg.Host != "203.0.113.10" {
		t.Fatalf("config host = %q, want 203.0.113.10", cfg.Host)
	}
	if cfg.Domain != "rascal.example.com" {
		t.Fatalf("config domain = %q, want rascal.example.com", cfg.Domain)
	}
	if cfg.Transport != "ssh" {
		t.Fatalf("config transport = %q, want ssh", cfg.Transport)
	}
	if cfg.SSHHost != "203.0.113.10" {
		t.Fatalf("config ssh_host = %q, want 203.0.113.10", cfg.SSHHost)
	}
	if cfg.APIToken != "api-token" {
		t.Fatalf("config api_token = %q, want api-token", cfg.APIToken)
	}

	var deployCfg deployConfig
	var deploySeedClient apiClient
	var deploySeedAuthPath string
	deployToExistingHostFn = func(cfg deployConfig) error {
		deployCfg = cfg
		return nil
	}
	waitForServerHealthSSHFn = func(cfg deployConfig, timeout time.Duration) error { return nil }
	seedBootstrapSharedCredentialFn = func(client apiClient, authFilePath string) (credentialRecord, error) {
		deploySeedClient = client
		deploySeedAuthPath = authFilePath
		return credentialRecord{}, nil
	}

	deployApp := &app{cfg: loadCfg(), configPath: configPath, output: "json", quiet: true}
	deployCmd := deployApp.newDeployCmd()
	deployCmd.SetOut(io.Discard)
	deployCmd.SetErr(io.Discard)
	deployCmd.SetArgs([]string{
		"--host", "203.0.113.10",
		"--goarch", "amd64",
		"--upload-env",
		"--github-runtime-token", "runtime-token",
		"--webhook-secret", "webhook-secret",
		"--codex-auth", authPath,
	})
	deployStdout, err := captureStdout(func() error { return deployCmd.Execute() })
	if err != nil {
		t.Fatalf("deploy execute: %v", err)
	}

	var deployOut deployCommandOutput
	if err := json.Unmarshal([]byte(deployStdout), &deployOut); err != nil {
		t.Fatalf("decode deploy output: %v\noutput:\n%s", err, deployStdout)
	}
	if deployOut.Host != "203.0.113.10" {
		t.Fatalf("deploy host = %q, want 203.0.113.10", deployOut.Host)
	}
	if deployOut.ServerURL != "https://rascal.example.com" {
		t.Fatalf("deploy server_url = %q, want https://rascal.example.com", deployOut.ServerURL)
	}
	if deployCfg.Host != "203.0.113.10" {
		t.Fatalf("deploy cfg host = %q, want 203.0.113.10", deployCfg.Host)
	}
	if deployCfg.GitHubRuntimeToken != "runtime-token" {
		t.Fatalf("deploy cfg github runtime token = %q, want runtime-token", deployCfg.GitHubRuntimeToken)
	}
	if deployCfg.WebhookSecret != "webhook-secret" {
		t.Fatalf("deploy cfg webhook secret = %q, want webhook-secret", deployCfg.WebhookSecret)
	}
	if deployCfg.APIToken != "api-token" {
		t.Fatalf("deploy cfg api token = %q, want api-token", deployCfg.APIToken)
	}
	if deploySeedAuthPath != authPath {
		t.Fatalf("seed auth path = %q, want %q", deploySeedAuthPath, authPath)
	}
	if deploySeedClient.token != "api-token" {
		t.Fatalf("seed client token = %q, want api-token", deploySeedClient.token)
	}
	if deploySeedClient.transport != "ssh" {
		t.Fatalf("seed client transport = %q, want ssh", deploySeedClient.transport)
	}
	if deploySeedClient.sshHost != "203.0.113.10" {
		t.Fatalf("seed client ssh host = %q, want 203.0.113.10", deploySeedClient.sshHost)
	}

	githubClient := &fakeRepoClient{}
	var githubAdminToken string
	newRepoGitHubClientFn = func(token string) repoGitHubClient {
		githubAdminToken = token
		return githubClient
	}

	githubApp := &app{cfg: loadCfg(), configPath: configPath, output: "json"}
	githubCmd := githubApp.newGitHubCmd()
	githubCmd.SetOut(io.Discard)
	githubCmd.SetErr(io.Discard)
	githubCmd.SetArgs([]string{
		"setup",
		"--github-admin-token", "admin-token",
		"--webhook-secret", "webhook-secret",
	})
	githubStdout, err := captureStdout(func() error { return githubCmd.Execute() })
	if err != nil {
		t.Fatalf("github setup execute: %v", err)
	}

	var githubOut repoEnableOutput
	if err := json.Unmarshal([]byte(githubStdout), &githubOut); err != nil {
		t.Fatalf("decode github setup output: %v\noutput:\n%s", err, githubStdout)
	}
	if githubAdminToken != "admin-token" {
		t.Fatalf("github admin token = %q, want admin-token", githubAdminToken)
	}
	if !githubClient.ensureCalled || !githubClient.webhookCalled {
		t.Fatalf("expected github label and webhook calls, got ensure=%t webhook=%t", githubClient.ensureCalled, githubClient.webhookCalled)
	}
	if githubClient.webhookRepo != "owner/repo" {
		t.Fatalf("github webhook repo = %q, want owner/repo", githubClient.webhookRepo)
	}
	if githubClient.webhookURL != "https://rascal.example.com/v1/webhooks/github" {
		t.Fatalf("github webhook url = %q, want https://rascal.example.com/v1/webhooks/github", githubClient.webhookURL)
	}
	if githubClient.webhookSecret != "webhook-secret" {
		t.Fatalf("github webhook secret = %q, want webhook-secret", githubClient.webhookSecret)
	}
	if githubOut.Repo != "owner/repo" || !githubOut.Enabled {
		t.Fatalf("unexpected github output: %+v", githubOut)
	}

	checkServerHealthFn = func(string) (bool, string) {
		t.Fatal("expected doctor to use ssh health check")
		return false, "unexpected http health check"
	}
	checkServerHealthSSHFn = func(cfg deployConfig) (bool, string) {
		if cfg.Host != "203.0.113.10" {
			t.Fatalf("doctor ssh host = %q, want 203.0.113.10", cfg.Host)
		}
		if cfg.SSHUser != "root" {
			t.Fatalf("doctor ssh user = %q, want root", cfg.SSHUser)
		}
		if cfg.SSHPort != 22 {
			t.Fatalf("doctor ssh port = %d, want 22", cfg.SSHPort)
		}
		return true, ""
	}

	doctorApp := &app{
		cfg:             loadCfg(),
		configPath:      configPath,
		output:          "json",
		serverSource:    "config",
		tokenSource:     "config",
		repoSource:      "config",
		transportSource: "config",
	}
	doctorCmd := doctorApp.newDoctorCmd()
	doctorCmd.SetOut(io.Discard)
	doctorCmd.SetErr(io.Discard)
	doctorStdout, err := captureStdout(func() error { return doctorCmd.Execute() })
	if err != nil {
		t.Fatalf("doctor execute: %v", err)
	}

	var doctorOut doctorDiagnostics
	if err := json.Unmarshal([]byte(doctorStdout), &doctorOut); err != nil {
		t.Fatalf("decode doctor output: %v\noutput:\n%s", err, doctorStdout)
	}
	if !doctorOut.ConfigExists {
		t.Fatal("expected doctor to report config file present")
	}
	if doctorOut.DefaultRepo != "owner/repo" {
		t.Fatalf("doctor default_repo = %q, want owner/repo", doctorOut.DefaultRepo)
	}
	if doctorOut.ServerURL != "https://rascal.example.com" {
		t.Fatalf("doctor server_url = %q, want https://rascal.example.com", doctorOut.ServerURL)
	}
	if doctorOut.ResolvedTransport != "ssh" {
		t.Fatalf("doctor resolved_transport = %q, want ssh", doctorOut.ResolvedTransport)
	}
	if doctorOut.EffectiveSSHHost != "203.0.113.10" {
		t.Fatalf("doctor effective_ssh_host = %q, want 203.0.113.10", doctorOut.EffectiveSSHHost)
	}
	if !doctorOut.ServerHealthOK {
		t.Fatalf("expected doctor server health ok, got false (%s)", doctorOut.ServerHealthError)
	}
}
