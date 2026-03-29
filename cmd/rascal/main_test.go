package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func TestDoctorJSONOutput(t *testing.T) {
	origCheckServerHealthFn := checkServerHealthFn
	origCheckServerHealthSSHFn := checkServerHealthSSHFn
	origRunRemoteDoctorFn := runRemoteDoctorFn
	t.Cleanup(func() {
		checkServerHealthFn = origCheckServerHealthFn
		checkServerHealthSSHFn = origCheckServerHealthSSHFn
		runRemoteDoctorFn = origRunRemoteDoctorFn
	})

	checkServerHealthFn = func(string) (bool, string) { return true, "" }
	checkServerHealthSSHFn = func(deployConfig) (bool, string) { return false, "unexpected ssh health call" }
	runRemoteDoctorFn = func(deployConfig) (remoteDoctorStatus, error) {
		t.Fatal("did not expect remote doctor call")
		return remoteDoctorStatus{}, nil
	}

	a := &app{
		cfg: config.ClientConfig{
			ServerURL:   "http://127.0.0.1:8080",
			APIToken:    "token",
			DefaultRepo: "owner/repo",
			Transport:   "http",
			SSHUser:     "root",
			SSHPort:     22,
		},
		output:       "json",
		configPath:   filepath.Join(t.TempDir(), "config.toml"),
		serverSource: "config",
		tokenSource:  "config",
		repoSource:   "config",
	}

	cmd := a.newDoctorCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("doctor execute: %v", err)
	}

	var out doctorDiagnostics
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode doctor output: %v\noutput:\n%s", err, stdout)
	}
	if !out.ServerHealthOK {
		t.Fatalf("expected server_health_ok=true, got false (%s)", out.ServerHealthError)
	}
	if out.Remote != nil {
		t.Fatalf("expected remote to be omitted, got %+v", out.Remote)
	}
	if out.DefaultRepo != "owner/repo" {
		t.Fatalf("default_repo = %q, want owner/repo", out.DefaultRepo)
	}
}

func TestDoctorJSONOutputIncludesTypedRemoteStatus(t *testing.T) {
	origCheckServerHealthFn := checkServerHealthFn
	origCheckServerHealthSSHFn := checkServerHealthSSHFn
	origRunRemoteDoctorFn := runRemoteDoctorFn
	t.Cleanup(func() {
		checkServerHealthFn = origCheckServerHealthFn
		checkServerHealthSSHFn = origCheckServerHealthSSHFn
		runRemoteDoctorFn = origRunRemoteDoctorFn
	})

	checkServerHealthFn = func(string) (bool, string) { return false, "health failed" }
	checkServerHealthSSHFn = func(deployConfig) (bool, string) { return false, "unexpected ssh health call" }
	runRemoteDoctorFn = func(cfg deployConfig) (remoteDoctorStatus, error) {
		if cfg.Host != "203.0.113.10" {
			t.Fatalf("host = %q, want 203.0.113.10", cfg.Host)
		}
		return remoteDoctorStatus{
			Host:                  cfg.Host,
			RascalService:         true,
			ActiveSlot:            "blue",
			DockerInstalled:       true,
			SQLiteInstalled:       true,
			CaddyInstalled:        true,
			EnvFilePresent:        true,
			AuthRuntimeSynced:     true,
			RunnerImageConfigured: true,
			RunnerImagePresent:    true,
			RunnerImageGooseCodex: "rascal-runner-goose-codex:latest",
			RunnerImageCodex:      "rascal-runner-codex:latest",
		}, nil
	}

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
			Transport: "http",
			SSHUser:   "root",
			SSHPort:   22,
		},
		output:     "json",
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}

	cmd := a.newDoctorCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--host", "203.0.113.10"})

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("doctor execute: %v", err)
	}

	var out doctorDiagnostics
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode doctor output: %v\noutput:\n%s", err, stdout)
	}
	if out.Remote == nil {
		t.Fatal("expected remote diagnostics")
	}
	if out.Remote.Error != "" {
		t.Fatalf("expected empty remote error, got %q", out.Remote.Error)
	}
	if out.Remote.Host != "203.0.113.10" {
		t.Fatalf("remote.host = %q, want 203.0.113.10", out.Remote.Host)
	}
	if !out.Remote.RascalService || out.Remote.ActiveSlot != "blue" {
		t.Fatalf("unexpected remote status: %+v", out.Remote)
	}
	if out.Remote.RunnerImageGooseCodex != "rascal-runner-goose-codex:latest" {
		t.Fatalf("runner_image_goose_codex = %q, want rascal-runner-goose-codex:latest", out.Remote.RunnerImageGooseCodex)
	}
}

func TestCompletionHelpContainsInstall(t *testing.T) {
	root := mustNewRootCmd(t)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"completion", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "install") {
		t.Fatalf("expected completion help to mention install subcommand\n%s", stdout.String())
	}
}

func TestRetryCommandAliasesRerun(t *testing.T) {
	root := mustNewRootCmd(t)
	cmd, _, err := root.Find([]string{"rerun"})
	if err != nil {
		t.Fatalf("find rerun: %v", err)
	}
	if cmd.Name() != "retry" {
		t.Fatalf("expected rerun alias to resolve to retry, got %q", cmd.Name())
	}
}

func TestRootFlagsDoNotExposeDeadFlags(t *testing.T) {
	root := mustNewRootCmd(t)
	for _, name := range []string{"verbose", "yes", "debug"} {
		if f := root.PersistentFlags().Lookup(name); f != nil {
			t.Fatalf("unexpected flag %q", name)
		}
	}
	if f := root.PersistentFlags().Lookup("quiet"); f == nil {
		t.Fatal("expected quiet flag to exist")
	}
	if f := root.PersistentFlags().Lookup("no-color"); f == nil {
		t.Fatal("expected no-color flag to exist")
	}
	for _, name := range []string{"transport", "client-ssh-host", "client-ssh-user", "client-ssh-key", "client-ssh-port"} {
		if f := root.PersistentFlags().Lookup(name); f == nil {
			t.Fatalf("expected %s flag to exist", name)
		}
	}
}

func TestRootHasGitHubAndSetupCommands(t *testing.T) {
	root := mustNewRootCmd(t)
	if _, _, err := root.Find([]string{"init"}); err != nil {
		t.Fatalf("init command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"deploy"}); err != nil {
		t.Fatalf("deploy command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"provision"}); err != nil {
		t.Fatalf("provision command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"github"}); err != nil {
		t.Fatalf("github command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"github", "setup"}); err != nil {
		t.Fatalf("github setup command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"github", "webhook", "test"}); err != nil {
		t.Fatalf("github webhook test command missing: %v", err)
	}
	if cmd, _, err := root.Find([]string{"infra"}); err == nil && cmd != nil && cmd.Name() == "infra" {
		t.Fatal("infra command should be removed")
	}
	if _, _, err := root.Find([]string{"logs", "run"}); err != nil {
		t.Fatalf("logs run command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"logs", "job"}); err != nil {
		t.Fatalf("logs job alias missing: %v", err)
	}
	if _, _, err := root.Find([]string{"logs", "rascald"}); err != nil {
		t.Fatalf("logs rascald command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"logs", "caddy"}); err != nil {
		t.Fatalf("logs caddy command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"logs", "caddy-access"}); err != nil {
		t.Fatalf("logs caddy-access command missing: %v", err)
	}

	for _, c := range root.Commands() {
		if c.Name() == "webhook" {
			t.Fatal("root webhook command should be removed")
		}
	}
}

func TestLogsFollowIntervalDefaults(t *testing.T) {
	root := mustNewRootCmd(t)

	logsCmd, _, err := root.Find([]string{"logs"})
	if err != nil {
		t.Fatalf("logs command missing: %v", err)
	}
	if got := logsCmd.Flags().Lookup("interval").DefValue; got != "4s" {
		t.Fatalf("logs default interval = %q, want 4s", got)
	}

	runLogsCmd, _, err := root.Find([]string{"logs", "run"})
	if err != nil {
		t.Fatalf("logs run command missing: %v", err)
	}
	if got := runLogsCmd.Flags().Lookup("interval").DefValue; got != "4s" {
		t.Fatalf("logs run default interval = %q, want 4s", got)
	}
}

func TestInitDeployAndProvisionDefaults(t *testing.T) {
	root := mustNewRootCmd(t)

	initCmd, _, err := root.Find([]string{"init"})
	if err != nil {
		t.Fatalf("init command missing: %v", err)
	}
	if got := initCmd.Flags().Lookup("goarch"); got != nil {
		t.Fatalf("init should not expose goarch flag, got %q", got.Name)
	}
	if got := initCmd.Flags().Lookup("hcloud-server-type"); got != nil {
		t.Fatalf("init should not expose hcloud-server-type flag, got %q", got.Name)
	}
	if got := initCmd.Flags().Lookup("print-plan"); got == nil {
		t.Fatal("init should expose print-plan flag")
	}
	if got := initCmd.Flags().Lookup("provision"); got == nil {
		t.Fatal("init should expose provision flag")
	}
	if got := initCmd.Flags().Lookup("skip-github"); got == nil {
		t.Fatal("init should expose skip-github flag")
	}
	if got := initCmd.Flags().Lookup("skip-webhook"); got != nil {
		t.Fatalf("init should not expose legacy --skip-webhook flag, got %q", got.Name)
	}
	if got := initCmd.Flags().Lookup("provision-new"); got != nil {
		t.Fatalf("init should not expose legacy --provision-new flag, got %q", got.Name)
	}

	deployCmd, _, err := root.Find([]string{"deploy"})
	if err != nil {
		t.Fatalf("deploy command missing: %v", err)
	}
	if got := deployCmd.Flags().Lookup("goarch").DefValue; got != "" {
		t.Fatalf("deploy default goarch = %q, want empty for auto-detect", got)
	}
	if got := deployCmd.Flags().Lookup("runner-image"); got != nil {
		t.Fatalf("deploy should not expose legacy --runner-image flag")
	}
	if got := deployCmd.Flags().Lookup("agent-runtime").DefValue; got != "" {
		t.Fatalf("deploy default agent-runtime = %q, want empty", got)
	}
	if got := deployCmd.Flags().Lookup("upload-env").DefValue; got != "false" {
		t.Fatalf("deploy default upload-env = %q, want false", got)
	}
	if got := deployCmd.Flags().Lookup("codex-auth").DefValue; got != "" {
		t.Fatalf("deploy default codex-auth = %q, want empty", got)
	}

	provisionCmd, _, err := root.Find([]string{"provision"})
	if err != nil {
		t.Fatalf("provision command missing: %v", err)
	}
	if got := provisionCmd.Flags().Lookup("server-type").DefValue; got != "cx23" {
		t.Fatalf("provision default server type = %q, want cx23", got)
	}
	if cmd, _, err := root.Find([]string{"bootstrap"}); err == nil && cmd != nil && cmd.Name() == "bootstrap" {
		t.Fatal("bootstrap command should be removed")
	}
	if cmd, _, err := root.Find([]string{"infra"}); err == nil && cmd != nil && cmd.Name() == "infra" {
		t.Fatal("infra command should be removed")
	}
}

func TestInitPrintPlanShowsMissingPrerequisites(t *testing.T) {
	tmp := t.TempDir()
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(tmp, "config.toml"),
	}
	for _, k := range []string{
		"RASCAL_API_TOKEN",
		"GITHUB_ADMIN_TOKEN",
		"RASCAL_GITHUB_TOKEN",
		"RASCAL_GITHUB_WEBHOOK_SECRET",
		"HCLOUD_TOKEN",
	} {
		t.Setenv(k, "")
	}

	missingAuthPath := filepath.Join(tmp, "missing-auth.json")
	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--print-plan",
		"--host", "203.0.113.10",
		"--repo", "owner/repo",
		"--codex-auth", missingAuthPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("init --print-plan failed: %v", err)
	}

	var out initPlanOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out.Status != "init_plan" {
		t.Fatalf("unexpected status: %v", out.Status)
	}
	if out.Ready {
		t.Fatalf("expected ready=false, got %v", out.Ready)
	}
	if !containsSubstring(out.Missing, "GitHub admin token is required") {
		t.Fatalf("missing list should include admin token requirement, got %v", out.Missing)
	}
	if !containsSubstring(out.Missing, "GitHub runtime token is required") {
		t.Fatalf("missing list should include runtime token requirement, got %v", out.Missing)
	}
	if !containsSubstring(out.Missing, "codex auth file not found") {
		t.Fatalf("missing list should include codex auth file check, got %v", out.Missing)
	}

	if !containsExact(out.Actions, "deploy rascald to 203.0.113.10 over SSH") {
		t.Fatalf("actions should include deploy step, got %v", out.Actions)
	}
	if !containsExact(out.Actions, "configure GitHub webhook and label for owner/repo") {
		t.Fatalf("actions should include webhook step, got %v", out.Actions)
	}
}

func TestInitPrintPlanReadyForProvisionFlow(t *testing.T) {
	tmp := t.TempDir()
	authPath := filepath.Join(tmp, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(tmp, "config.toml"),
	}
	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--print-plan",
		"--repo", "owner/repo",
		"--provision",
		"--hcloud-token", "hcloud-token",
		"--github-admin-token", "admin-token",
		"--github-runtime-token", "runtime-token",
		"--codex-auth", authPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("init --print-plan failed: %v", err)
	}

	var out initPlanOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if !out.Ready {
		t.Fatalf("expected ready=true, got %v", out.Ready)
	}
	if len(out.Missing) != 0 {
		t.Fatalf("expected no missing prerequisites, got %v", out.Missing)
	}
	if got := out.Resolved.ServerURL; got != "http://<provisioned-host>:8080" {
		t.Fatalf("unexpected server_url: %v", got)
	}
	if got := out.Resolved.ServerURLSource; got != "provisioned_host" {
		t.Fatalf("unexpected server_url_source: %v", got)
	}
}

func TestInitPrintPlanUsesCanonicalRuntimeTokenEnv(t *testing.T) {
	tmp := t.TempDir()
	authPath := filepath.Join(tmp, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(tmp, "config.toml"),
	}
	t.Setenv("GITHUB_ADMIN_TOKEN", "admin-token")
	t.Setenv("RASCAL_GITHUB_TOKEN", "runtime-token")

	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--print-plan",
		"--host", "203.0.113.10",
		"--repo", "owner/repo",
		"--codex-auth", authPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("init --print-plan failed: %v", err)
	}

	var out initPlanOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if !out.Ready {
		t.Fatalf("expected ready=true, got %v", out.Ready)
	}
	if len(out.Missing) != 0 {
		t.Fatalf("expected no missing prerequisites, got %v", out.Missing)
	}
}

func TestInitPrintPlanIgnoresLegacyRuntimeTokenEnv(t *testing.T) {
	tmp := t.TempDir()
	authPath := filepath.Join(tmp, "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"ok":true}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(tmp, "config.toml"),
	}
	t.Setenv("GITHUB_ADMIN_TOKEN", "admin-token")
	t.Setenv("RASCAL_GITHUB_TOKEN", "")

	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--print-plan",
		"--host", "203.0.113.10",
		"--repo", "owner/repo",
		"--codex-auth", authPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("init --print-plan failed: %v", err)
	}

	var out initPlanOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out.Ready {
		t.Fatalf("expected ready=false, got %v", out.Ready)
	}
	if !containsSubstring(out.Missing, "GitHub runtime token is required") {
		t.Fatalf("missing list should include runtime token requirement, got %v", out.Missing)
	}
}

func TestInitStillValidatesWithoutPrintPlan(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}
	for _, k := range []string{
		"GITHUB_ADMIN_TOKEN",
		"RASCAL_GITHUB_TOKEN",
	} {
		t.Setenv(k, "")
	}

	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--host", "203.0.113.10",
		"--repo", "owner/repo",
		"--github-runtime-token", "runtime-token",
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected init to fail when GitHub admin token is missing")
	}
	if !strings.Contains(err.Error(), "--github-admin-token is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestInitCompletionJSONOutput(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}

	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--skip-deploy",
		"--skip-github",
		"--server-url", "https://rascal.example.com",
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("init: %v", err)
	}

	var out initCompleteOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out.Status != "init_complete" {
		t.Fatalf("status = %q, want init_complete", out.Status)
	}
	if out.ServerURL != "https://rascal.example.com" {
		t.Fatalf("server_url = %q, want https://rascal.example.com", out.ServerURL)
	}
	if out.Host != "" {
		t.Fatalf("host = %q, want empty", out.Host)
	}
	if out.APIToken != "" {
		t.Fatalf("expected empty api token when not deploying, got %q", out.APIToken)
	}
	if out.WebhookSecret != "" {
		t.Fatalf("expected empty webhook secret when GitHub setup is skipped, got %q", out.WebhookSecret)
	}
}

func TestInitValidatesProvisionAndGitHubInputs(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
	}
	cmd := a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	cmd.SetArgs([]string{"--host", "203.0.113.10", "--provision"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected init to fail when host and provision are combined")
	}
	if !strings.Contains(err.Error(), "--host cannot be combined with --provision") {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd = a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	t.Setenv("HCLOUD_TOKEN", "")
	cmd.SetArgs([]string{"--provision"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected init to fail without hcloud token in provision mode")
	}
	if !strings.Contains(err.Error(), "required when --provision is set") {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd = a.newInitCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	t.Setenv("GITHUB_ADMIN_TOKEN", "")
	cmd.SetArgs([]string{"--repo", "owner/repo", "--server-url", "https://rascal.example.com"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected init to fail when GitHub setup is enabled without admin token")
	}
	if !strings.Contains(err.Error(), "--github-admin-token is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunRetryDebugDefaults(t *testing.T) {
	root := mustNewRootCmd(t)

	runCmd, _, err := root.Find([]string{"run"})
	if err != nil {
		t.Fatalf("run command missing: %v", err)
	}
	if got := runCmd.Flags().Lookup("debug").DefValue; got != "true" {
		t.Fatalf("run default debug = %q, want true", got)
	}

	retryCmd, _, err := root.Find([]string{"retry"})
	if err != nil {
		t.Fatalf("retry command missing: %v", err)
	}
	if got := retryCmd.Flags().Lookup("debug").DefValue; got != "true" {
		t.Fatalf("retry default debug = %q, want true", got)
	}
}

func TestPSDefaults(t *testing.T) {
	root := mustNewRootCmd(t)

	psCmd, _, err := root.Find([]string{"ps"})
	if err != nil {
		t.Fatalf("ps command missing: %v", err)
	}
	if got := psCmd.Flags().Lookup("limit").DefValue; got != "10" {
		t.Fatalf("ps default limit = %q, want 10", got)
	}
	if got := psCmd.Flags().Lookup("all").DefValue; got != "false" {
		t.Fatalf("ps default all = %q, want false", got)
	}
	if got := psCmd.Flags().Lookup("status").DefValue; got != "" {
		t.Fatalf("ps default status = %q, want empty", got)
	}
}

func TestPSUsesDefaultLimitQuery(t *testing.T) {
	var gotLimit, gotAll string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotLimit = r.URL.Query().Get("limit")
		gotAll = r.URL.Query().Get("all")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunsResponse{Runs: []state.Run{}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("ps execute: %v", err)
	}
	if gotLimit != "10" {
		t.Fatalf("expected limit=10, got %q", gotLimit)
	}
	if gotAll != "" {
		t.Fatalf("expected all query empty, got %q", gotAll)
	}
}

func TestPSAllUsesAllQuery(t *testing.T) {
	var gotLimit, gotAll string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		gotLimit = r.URL.Query().Get("limit")
		gotAll = r.URL.Query().Get("all")
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunsResponse{Runs: []state.Run{}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--all"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("ps --all execute: %v", err)
	}
	if gotAll != "1" {
		t.Fatalf("expected all=1, got %q", gotAll)
	}
	if gotLimit != "" {
		t.Fatalf("expected limit query empty when --all is used, got %q", gotLimit)
	}
}

func TestPSAllCannotBeCombinedWithLimit(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			APIToken: "test-token",
		},
	}
	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--all", "--limit", "25"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error when --all and --limit are combined")
	}
	if !strings.Contains(err.Error(), "--all cannot be combined with --limit") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestParsePSStatusFilter(t *testing.T) {
	got, err := parsePSStatusFilter(" Running,review,FAILED,canceled ")
	if err != nil {
		t.Fatalf("parsePSStatusFilter unexpected error: %v", err)
	}
	if len(got) != 4 {
		t.Fatalf("expected 4 statuses, got %d", len(got))
	}
	for _, status := range []state.RunStatus{
		state.StatusRunning,
		state.StatusReview,
		state.StatusFailed,
		state.StatusCanceled,
	} {
		if _, ok := got[status]; !ok {
			t.Fatalf("expected normalized status %q to be present", status)
		}
	}
}

func TestParsePSStatusFilterInvalidValue(t *testing.T) {
	_, err := parsePSStatusFilter("running,not_real")
	if err == nil {
		t.Fatal("expected parsePSStatusFilter to fail")
	}
	if !strings.Contains(err.Error(), `invalid --status value "not_real"`) {
		t.Fatalf("unexpected error: %v", err)
	}
	if !strings.Contains(err.Error(), "allowed values: queued, running, review, succeeded, failed, canceled") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPSStatusFilterRendersOnlyMatchingRuns(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunsResponse{Runs: []state.Run{
			{ID: "run_queued", Status: state.StatusQueued, Repo: "owner/repo", CreatedAt: time.Date(2026, 3, 8, 13, 55, 0, 0, time.UTC)},
			{ID: "run_running", Status: state.StatusRunning, Repo: "owner/repo", CreatedAt: time.Date(2026, 3, 8, 13, 56, 0, 0, time.UTC)},
			{ID: "run_review", Status: state.StatusReview, Repo: "owner/repo", CreatedAt: time.Date(2026, 3, 8, 13, 57, 0, 0, time.UTC)},
		}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "table",
	}

	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--status", "running,REVIEW"})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("ps --status execute: %v", err)
	}
	if strings.Contains(stdout, "run_queued") {
		t.Fatalf("expected queued run to be filtered out, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "run_running") {
		t.Fatalf("expected running run to remain, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "run_review") {
		t.Fatalf("expected review filter to include review run, got:\n%s", stdout)
	}
}

func TestPSStatusFilterInvalidValueReturnsInputError(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			APIToken: "test-token",
		},
	}
	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--status", "running,unknown"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid --status")
	}
	if !strings.Contains(err.Error(), `invalid --status value "unknown"`) {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestPSStatusAndPRLabels(t *testing.T) {
	run := state.Run{Status: state.StatusReview, PRNumber: 77}
	if got := string(run.Status); got != "review" {
		t.Fatalf("run status = %q, want review", got)
	}
	if got := psPRLabel(run); got != "#77" {
		t.Fatalf("psPRLabel review without pr_status = %q, want #77", got)
	}

	run = state.Run{Status: state.StatusReview, PRNumber: 77, PRStatus: state.PRStatusOpen}
	if got := psPRLabel(run); got != "#77 open" {
		t.Fatalf("psPRLabel open = %q, want #77 open", got)
	}

	run = state.Run{Status: state.StatusSucceeded, PRNumber: 77, PRStatus: state.PRStatusMerged}
	if got := psPRLabel(run); got != "#77 merged" {
		t.Fatalf("psPRLabel succeeded = %q, want #77 merged", got)
	}

	run = state.Run{Status: state.StatusCanceled, PRNumber: 77, PRStatus: state.PRStatusClosedUnmerged}
	if got := psPRLabel(run); got != "#77 closed" {
		t.Fatalf("psPRLabel canceled = %q, want #77 closed", got)
	}

	run = state.Run{Status: state.StatusQueued}
	if got := psPRLabel(run); got != "-" {
		t.Fatalf("psPRLabel no pr = %q, want -", got)
	}

	run = state.Run{Status: state.StatusQueued, PRURL: "https://example.com/pr/77"}
	if got := psPRLabel(run); got != "link" {
		t.Fatalf("psPRLabel pr_url-only = %q, want link", got)
	}

	run = state.Run{Status: state.StatusQueued, PRURL: "https://example.com/pr/77", PRStatus: state.PRStatusOpen}
	if got := psPRLabel(run); got != "open" {
		t.Fatalf("psPRLabel pr_url-only open = %q, want open", got)
	}
}

func TestPSCreatedLabelFormatsUTC(t *testing.T) {
	createdAt := time.Date(2026, 3, 5, 20, 15, 42, 0, time.FixedZone("UTC+1", 1*60*60))
	if got := psCreatedLabel(createdAt); got != "2026-03-05 19:15" {
		t.Fatalf("psCreatedLabel = %q, want 2026-03-05 19:15", got)
	}
}

func TestPSIssueLabel(t *testing.T) {
	if got := psIssueLabel(state.Run{IssueNumber: 119}); got != "#119" {
		t.Fatalf("psIssueLabel issue = %q, want #119", got)
	}
	if got := psIssueLabel(state.Run{}); got != "" {
		t.Fatalf("psIssueLabel empty = %q, want empty", got)
	}
}

func TestPSRendersIssueColumn(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/runs" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunsResponse{Runs: []state.Run{
			{ID: "run_issue", Status: state.StatusFailed, Repo: "owner/repo", IssueNumber: 119, CreatedAt: time.Date(2026, 3, 8, 13, 56, 0, 0, time.UTC)},
			{ID: "run_no_issue", Status: state.StatusRunning, Repo: "owner/repo", CreatedAt: time.Date(2026, 3, 8, 13, 57, 0, 0, time.UTC)},
		}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "table",
	}

	cmd := a.newPSCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("ps execute: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(stdout), "\n")
	if len(lines) != 3 {
		t.Fatalf("expected header plus two rows, got:\n%s", stdout)
	}
	if got := strings.Fields(lines[0]); strings.Join(got, " ") != "RUN ID STATUS REPO ISSUE PR CREATED (UTC)" {
		t.Fatalf("unexpected header fields: %q", got)
	}
	if got := strings.Fields(lines[1]); strings.Join(got, " ") != "run_issue failed owner/repo #119 - 2026-03-08 13:56" {
		t.Fatalf("unexpected issue-backed row fields: %q", got)
	}
	if !strings.Contains(lines[2], "run_no_issue") || !strings.Contains(lines[2], "owner/repo") || !strings.Contains(lines[2], "2026-03-08 13:57") {
		t.Fatalf("expected non-issue row content, got:\n%s", lines[2])
	}
	if strings.Contains(lines[2], "#") {
		t.Fatalf("expected blank issue column for non-issue run, got:\n%s", lines[2])
	}
}

func TestRunCreatesTaskPayload(t *testing.T) {
	var payload api.CreateTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/tasks" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_new", Status: state.StatusQueued}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := mustNewRunCmd(t, a)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--repo", "owner/repo", "--instruction", "Fix flaky tests"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run --repo --instruction: %v", err)
	}

	if payload.Repo != "owner/repo" {
		t.Fatalf("unexpected repo payload: %v", payload.Repo)
	}
	if payload.Instruction != "Fix flaky tests" {
		t.Fatalf("unexpected task payload: %v", payload.Instruction)
	}
	if payload.BaseBranch != "main" {
		t.Fatalf("expected default base_branch main, got: %v", payload.BaseBranch)
	}
	if payload.Debug != nil {
		t.Fatalf("did not expect debug payload unless flag is set, got: %v", payload.Debug)
	}
}

func TestRunIssueCreatesIssueRunPayload(t *testing.T) {
	var payload api.CreateIssueTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/tasks/issue" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_issue", Status: state.StatusQueued}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := mustNewRunCmd(t, a)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--issue", "owner/repo#123"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run --issue: %v", err)
	}

	if payload.Repo != "owner/repo" {
		t.Fatalf("unexpected repo payload: %v", payload.Repo)
	}
	if payload.IssueNumber != 123 {
		t.Fatalf("unexpected issue number payload: %v", payload.IssueNumber)
	}
	if payload.Debug != nil {
		t.Fatalf("did not expect debug payload unless flag is set, got: %v", payload.Debug)
	}
}

func TestRunIssueSendsExplicitDebugOverride(t *testing.T) {
	var payload api.CreateIssueTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/tasks/issue" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if err := json.Unmarshal(body, &payload); err != nil {
			t.Fatalf("decode payload: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_issue", Status: state.StatusQueued}}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := mustNewRunCmd(t, a)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--issue", "owner/repo#123", "--debug=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run --issue --debug=false: %v", err)
	}

	if payload.Debug == nil || *payload.Debug != false {
		t.Fatalf("expected explicit debug override false, got: %v", payload.Debug)
	}
}

func TestRetryOmitsDebugByDefault(t *testing.T) {
	var retryPayload api.CreateTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_old":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{
				ID: "run_old", TaskID: "task_1", Repo: "owner/repo", Instruction: "Fix it", BaseBranch: "main", Status: state.StatusCanceled,
			}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read retry body: %v", err)
			}
			if err := json.Unmarshal(body, &retryPayload); err != nil {
				t.Fatalf("decode retry payload: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_retry", Status: state.StatusQueued}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			return
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := a.newRetryCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run_old"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry: %v", err)
	}

	if retryPayload.Debug != nil {
		t.Fatalf("did not expect debug payload unless flag is set, got: %v", retryPayload.Debug)
	}
}

func TestRetrySendsExplicitDebugOverride(t *testing.T) {
	var retryPayload api.CreateTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_old":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{
				ID: "run_old", TaskID: "task_1", Repo: "owner/repo", Instruction: "Fix it", BaseBranch: "main", Status: state.StatusCanceled,
			}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			return
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read retry body: %v", err)
			}
			if err := json.Unmarshal(body, &retryPayload); err != nil {
				t.Fatalf("decode retry payload: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_retry", Status: state.StatusQueued}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
			return
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := a.newRetryCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run_old", "--debug=false"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry --debug=false: %v", err)
	}

	if retryPayload.Debug == nil || *retryPayload.Debug != false {
		t.Fatalf("expected explicit debug override false, got: %v", retryPayload.Debug)
	}
}

func TestRetryCreatesRunWithRetryTrigger(t *testing.T) {
	var createPayload api.CreateTaskRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/runs/run_old":
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{
				ID: "run_old", TaskID: "owner/repo#123", Repo: "owner/repo", Instruction: "Fix failing tests", BaseBranch: "main", Status: state.StatusFailed,
			}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
		case r.Method == http.MethodPost && r.URL.Path == "/v1/tasks":
			body, err := io.ReadAll(r.Body)
			if err != nil {
				t.Fatalf("read retry create body: %v", err)
			}
			if err := json.Unmarshal(body, &createPayload); err != nil {
				t.Fatalf("decode retry payload: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(api.RunResponse{Run: state.Run{ID: "run_new", Status: state.StatusQueued}}); err != nil {
				t.Fatalf("encode response: %v", err)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
		output: "json",
	}

	cmd := a.newRetryCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"run_old"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("retry command failed: %v", err)
	}

	if createPayload.Trigger != runtrigger.NameRetry {
		t.Fatalf("retry payload trigger = %v, want retry", createPayload.Trigger)
	}
	if createPayload.SourceRunID != "run_old" {
		t.Fatalf("retry payload source_run_id = %q, want run_old", createPayload.SourceRunID)
	}
}

func TestRunIssueInvalidFormat(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			APIToken: "test-token",
		},
	}
	cmd := mustNewRunCmd(t, a)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--issue", "owner/repo"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for invalid issue ref")
	}
	if !strings.Contains(err.Error(), "invalid issue ref") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunIssueMutualExclusion(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			APIToken: "test-token",
		},
	}
	cmd := mustNewRunCmd(t, a)
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--issue", "owner/repo#42", "--instruction", "Fix it"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "--issue cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthHelpContainsSync(t *testing.T) {
	root := mustNewRootCmd(t)
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"auth", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "sync") {
		t.Fatalf("expected auth help to include sync command\n%s", stdout.String())
	}
}

func TestConfigUnsetRemovesKeyAndReportsEffectiveValue(t *testing.T) {
	clearEnvKeys(t,
		"RASCAL_SERVER_URL",
		"RASCAL_API_TOKEN",
		"RASCAL_DEFAULT_REPO",
		"RASCAL_HOST",
		"RASCAL_DOMAIN",
		"RASCAL_TRANSPORT",
		"RASCAL_SSH_HOST",
		"RASCAL_SSH_USER",
		"RASCAL_SSH_KEY",
		"RASCAL_SSH_PORT",
	)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	root := mustNewRootCmd(t)
	root.SetArgs([]string{"--config", cfgPath, "config", "set", "server_url", "https://example.com"})
	if err := root.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}
	if settings := readClientConfigFileForTest(t, cfgPath); settings.ServerURL == nil {
		t.Fatal("expected server_url to exist in config file after set")
	}

	stdout, err := captureStdout(func() error {
		root := mustNewRootCmd(t)
		root.SetArgs([]string{"--config", cfgPath, "--output", "json", "config", "unset", "server_url"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("config unset: %v", err)
	}

	var out struct {
		Status string `json:"status"`
		Source string `json:"source"`
		Value  string `json:"value"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if out.Status != "removed" {
		t.Fatalf("expected status removed, got %v", out.Status)
	}
	if out.Source != "default" {
		t.Fatalf("expected source default, got %v", out.Source)
	}
	if out.Value != "http://127.0.0.1:8080" {
		t.Fatalf("expected default server_url, got %v", out.Value)
	}
	if settings := readClientConfigFileForTest(t, cfgPath); settings.ServerURL != nil {
		t.Fatal("expected server_url to be removed from config file after unset")
	}
}

func TestConfigUnsetIdempotent(t *testing.T) {
	clearEnvKeys(t,
		"RASCAL_SERVER_URL",
		"RASCAL_API_TOKEN",
		"RASCAL_DEFAULT_REPO",
		"RASCAL_HOST",
		"RASCAL_DOMAIN",
		"RASCAL_TRANSPORT",
		"RASCAL_SSH_HOST",
		"RASCAL_SSH_USER",
		"RASCAL_SSH_KEY",
		"RASCAL_SSH_PORT",
	)
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	for i := 0; i < 2; i++ {
		stdout, err := captureStdout(func() error {
			root := mustNewRootCmd(t)
			root.SetArgs([]string{"--config", cfgPath, "--output", "json", "config", "unset", "default_repo"})
			return root.Execute()
		})
		if err != nil {
			t.Fatalf("config unset attempt %d: %v", i+1, err)
		}
		var out struct {
			Status  string `json:"status"`
			Message string `json:"message"`
		}
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("invalid json output attempt %d: %v", i+1, err)
		}
		if out.Status != "absent" {
			t.Fatalf("expected status absent attempt %d, got %v", i+1, out.Status)
		}
		if strings.TrimSpace(out.Message) == "" {
			t.Fatalf("expected message to be set attempt %d, got %v", i+1, out.Message)
		}
	}
	if _, err := os.Stat(cfgPath); err == nil || !os.IsNotExist(err) {
		t.Fatalf("expected config file to remain absent, got err=%v", err)
	}
}

func TestConfigGetMasksAPITokenFromLocalConfig(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")
	if err := config.SaveClientConfig(cfgPath, config.ClientConfig{APIToken: "secret-token-value"}); err != nil {
		t.Fatalf("SaveClientConfig: %v", err)
	}

	stdout, err := captureStdout(func() error {
		root := mustNewRootCmd(t)
		root.SetArgs([]string{"--config", cfgPath, "config", "get", "api_token"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("config get: %v", err)
	}

	if got := strings.TrimSpace(stdout); got != maskSecret("secret-token-value") {
		t.Fatalf("expected masked token, got %q", got)
	}
}

func TestConfigPathPrintsConfiguredPath(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	stdout, err := captureStdout(func() error {
		root := mustNewRootCmd(t)
		root.SetArgs([]string{"--config", cfgPath, "config", "path"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("config path: %v", err)
	}

	if got := strings.TrimSpace(stdout); got != cfgPath {
		t.Fatalf("config path output = %q, want %q", got, cfgPath)
	}
}

func TestConfigPathJSONOutput(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	stdout, err := captureStdout(func() error {
		root := mustNewRootCmd(t)
		root.SetArgs([]string{"--config", cfgPath, "--output", "json", "config", "path"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("config path json: %v", err)
	}

	var out struct {
		ConfigPath string `json:"config_path"`
	}
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if out.ConfigPath != cfgPath {
		t.Fatalf("config_path = %q, want %q", out.ConfigPath, cfgPath)
	}
}

func TestConfigSetSSHPortPersistsInteger(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	root := mustNewRootCmd(t)
	root.SetArgs([]string{"--config", cfgPath, "config", "set", "ssh_port", "2222"})
	if err := root.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}

	cfg, err := loadFileConfig(cfgPath)
	if err != nil {
		t.Fatalf("loadFileConfig: %v", err)
	}
	if cfg.SSHPort != 2222 {
		t.Fatalf("expected ssh_port 2222, got %d", cfg.SSHPort)
	}
}

func TestHelpGoldenSnapshots(t *testing.T) {
	cases := []struct {
		name string
		args []string
	}{
		{name: "root", args: nil},
		{name: "run", args: []string{"run"}},
		{name: "logs", args: []string{"logs"}},
		{name: "init", args: []string{"init"}},
		{name: "deploy", args: []string{"deploy"}},
		{name: "provision", args: []string{"provision"}},
		{name: "auth", args: []string{"auth"}},
		{name: "auth_credentials", args: []string{"auth", "credentials"}},
		{name: "github", args: []string{"github"}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := renderHelpOutput(t, tc.args...)
			goldenPath := filepath.Join("testdata", "help", tc.name+".golden")
			if os.Getenv("UPDATE_GOLDEN") == "1" {
				if err := os.MkdirAll(filepath.Dir(goldenPath), 0o755); err != nil {
					t.Fatalf("mkdir golden dir: %v", err)
				}
				if err := os.WriteFile(goldenPath, []byte(got), 0o644); err != nil {
					t.Fatalf("write golden: %v", err)
				}
				return
			}
			wantRaw, err := os.ReadFile(goldenPath)
			if err != nil {
				t.Fatalf("read golden %s: %v", goldenPath, err)
			}
			want := normalizeHelpOutput(string(wantRaw))
			if got != want {
				t.Fatalf("help output mismatch for %s\n--- want ---\n%s\n--- got ---\n%s", tc.name, want, got)
			}
		})
	}
}
