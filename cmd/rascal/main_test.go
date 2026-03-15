package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/pelletier/go-toml/v2"
	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/runtrigger"
	"github.com/rtzll/rascal/internal/state"
)

func TestMaskSecret(t *testing.T) {
	t.Parallel()

	cases := []struct {
		in   string
		want string
	}{
		{"", ""},
		{"abcd", "****"},
		{"abcdefgh", "********"},
		{"abcdefghijkl", "abcd****ijkl"},
	}

	for _, tc := range cases {
		got := maskSecret(tc.in)
		if got != tc.want {
			t.Fatalf("maskSecret(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

func TestFirstNonEmpty(t *testing.T) {
	t.Parallel()

	got := firstNonEmpty("", "   ", "x", "y")
	if got != "x" {
		t.Fatalf("firstNonEmpty unexpected: %q", got)
	}
}

func TestBuildCreateTaskPayloadForRun(t *testing.T) {
	t.Parallel()

	req := buildCreateTaskPayload(createTaskPayloadInput{
		Repo:       "owner/repo",
		Task:       "Fix flaky tests",
		BaseBranch: "main",
	})

	if req.path != "/v1/tasks" {
		t.Fatalf("path = %q, want /v1/tasks", req.path)
	}
	if req.task == nil {
		t.Fatal("expected task payload")
	}
	if req.issueTask != nil {
		t.Fatal("did not expect issue payload")
	}
	if req.task.Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", req.task.Repo)
	}
	if req.task.Task != "Fix flaky tests" {
		t.Fatalf("task = %q, want Fix flaky tests", req.task.Task)
	}
	if req.task.BaseBranch != "main" {
		t.Fatalf("base_branch = %q, want main", req.task.BaseBranch)
	}
	if req.task.TaskID != "" {
		t.Fatalf("did not expect task_id in run payload")
	}
	if req.task.Trigger != "" {
		t.Fatalf("did not expect trigger in run payload")
	}
	if req.task.Debug != nil {
		t.Fatalf("did not expect debug in run payload when unset")
	}
}

func TestBuildCreateTaskPayloadForRetry(t *testing.T) {
	t.Parallel()

	debug := false
	req := buildCreateTaskPayload(createTaskPayloadInput{
		TaskID:     "task_1",
		Repo:       "owner/repo",
		Task:       "Retry task",
		BaseBranch: "main",
		Trigger:    runtrigger.NameRetry,
		Debug:      &debug,
	})

	if req.path != "/v1/tasks" {
		t.Fatalf("path = %q, want /v1/tasks", req.path)
	}
	if req.task == nil {
		t.Fatal("expected task payload")
	}
	if req.task.TaskID != "task_1" {
		t.Fatalf("task_id = %q, want task_1", req.task.TaskID)
	}
	if req.task.Trigger != "retry" {
		t.Fatalf("trigger = %q, want retry", req.task.Trigger)
	}
	if req.task.Debug == nil || *req.task.Debug != false {
		t.Fatalf("debug = %v, want false", req.task.Debug)
	}
}

func TestBuildCreateTaskPayloadForIssue(t *testing.T) {
	t.Parallel()

	debug := true
	req := buildCreateTaskPayload(createTaskPayloadInput{
		Repo:        "owner/repo",
		IssueNumber: 42,
		Task:        "ignored",
		BaseBranch:  "ignored",
		Trigger:     runtrigger.Name("ignored"),
		Debug:       &debug,
	})

	if req.path != "/v1/tasks/issue" {
		t.Fatalf("path = %q, want /v1/tasks/issue", req.path)
	}
	if req.issueTask == nil {
		t.Fatal("expected issue payload")
	}
	if req.task != nil {
		t.Fatal("did not expect task payload")
	}
	if req.issueTask.Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", req.issueTask.Repo)
	}
	if req.issueTask.IssueNumber != 42 {
		t.Fatalf("issue_number = %d, want 42", req.issueTask.IssueNumber)
	}
	if req.issueTask.Debug == nil || *req.issueTask.Debug != true {
		t.Fatalf("debug = %v, want true", req.issueTask.Debug)
	}
}

func TestDecodeServerErrorIncludesRequestID(t *testing.T) {
	t.Parallel()

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{"X-Request-Id": []string{"req_123"}},
		Body:       io.NopCloser(strings.NewReader("upstream broke")),
	}

	err := decodeServerError(resp)
	ce, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if ce.RequestID != "req_123" {
		t.Fatalf("unexpected request id: %q", ce.RequestID)
	}
}

func TestEmitJSONOutput(t *testing.T) {
	a := &app{output: "json"}
	stdout, err := captureStdout(func() error {
		return emit(a, map[string]any{"ok": true}, nil)
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if out["ok"] != true {
		t.Fatalf("unexpected output: %v", out)
	}
}

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
			RunnerImageGoose:      "rascal-runner-goose:latest",
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
	if out.Remote.RunnerImageGoose != "rascal-runner-goose:latest" {
		t.Fatalf("runner_image_goose = %q, want rascal-runner-goose:latest", out.Remote.RunnerImageGoose)
	}
}

func TestCompletionHelpContainsInstall(t *testing.T) {
	root := newRootCmd()
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
	root := newRootCmd()
	cmd, _, err := root.Find([]string{"rerun"})
	if err != nil {
		t.Fatalf("find rerun: %v", err)
	}
	if cmd.Name() != "retry" {
		t.Fatalf("expected rerun alias to resolve to retry, got %q", cmd.Name())
	}
}

func TestRootFlagsDoNotExposeDeadFlags(t *testing.T) {
	root := newRootCmd()
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

func TestRootHasGitHubAndInfraCommands(t *testing.T) {
	root := newRootCmd()
	if _, _, err := root.Find([]string{"deploy"}); err != nil {
		t.Fatalf("deploy command missing: %v", err)
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
	if _, _, err := root.Find([]string{"infra"}); err != nil {
		t.Fatalf("infra command missing: %v", err)
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
	root := newRootCmd()

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

func TestBootstrapAndInfraDefaults(t *testing.T) {
	root := newRootCmd()

	bootstrapCmd, _, err := root.Find([]string{"bootstrap"})
	if err != nil {
		t.Fatalf("bootstrap command missing: %v", err)
	}
	if got := bootstrapCmd.Flags().Lookup("goarch"); got != nil {
		t.Fatalf("bootstrap should not expose goarch flag, got %q", got.Name)
	}
	if got := bootstrapCmd.Flags().Lookup("hcloud-server-type"); got != nil {
		t.Fatalf("bootstrap should not expose hcloud-server-type flag, got %q", got.Name)
	}
	if got := bootstrapCmd.Flags().Lookup("print-plan"); got == nil {
		t.Fatal("bootstrap should expose print-plan flag")
	}

	deployCmd, _, err := root.Find([]string{"deploy"})
	if err != nil {
		t.Fatalf("deploy command missing: %v", err)
	}
	if got := deployCmd.Flags().Lookup("goarch").DefValue; got != "" {
		t.Fatalf("deploy default goarch = %q, want empty for auto-detect", got)
	}
	if got := deployCmd.Flags().Lookup("runner-image").DefValue; got != "rascal-runner-goose:latest" {
		t.Fatalf("deploy default runner-image = %q, want rascal-runner-goose:latest", got)
	}
	if got := deployCmd.Flags().Lookup("agent-backend").DefValue; got != "codex" {
		t.Fatalf("deploy default agent-backend = %q, want codex", got)
	}
	if got := deployCmd.Flags().Lookup("upload-env").DefValue; got != "false" {
		t.Fatalf("deploy default upload-env = %q, want false", got)
	}
	if got := deployCmd.Flags().Lookup("codex-auth").DefValue; got != "" {
		t.Fatalf("deploy default codex-auth = %q, want empty", got)
	}

	provisionCmd, _, err := root.Find([]string{"infra", "provision-hetzner"})
	if err != nil {
		t.Fatalf("infra provision-hetzner command missing: %v", err)
	}
	if got := provisionCmd.Flags().Lookup("server-type").DefValue; got != "cx23" {
		t.Fatalf("infra provision default server type = %q, want cx23", got)
	}

	infraDeployCmd, _, err := root.Find([]string{"infra", "deploy-existing"})
	if err != nil {
		t.Fatalf("infra deploy-existing command missing: %v", err)
	}
	if got := infraDeployCmd.Flags().Lookup("goarch").DefValue; got != "" {
		t.Fatalf("infra deploy-existing default goarch = %q, want empty for auto-detect", got)
	}
	if got := infraDeployCmd.Flags().Lookup("runner-image").DefValue; got != "rascal-runner-goose:latest" {
		t.Fatalf("infra deploy-existing default runner-image = %q, want rascal-runner-goose:latest", got)
	}
	if got := infraDeployCmd.Flags().Lookup("agent-backend").DefValue; got != "codex" {
		t.Fatalf("infra deploy-existing default agent-backend = %q, want codex", got)
	}
	if got := infraDeployCmd.Flags().Lookup("upload-env").DefValue; got != "false" {
		t.Fatalf("infra deploy-existing default upload-env = %q, want false", got)
	}
	if got := infraDeployCmd.Flags().Lookup("codex-auth").DefValue; got != "" {
		t.Fatalf("infra deploy-existing default codex-auth = %q, want empty", got)
	}

	infraUpCmd, _, err := root.Find([]string{"infra", "up"})
	if err != nil {
		t.Fatalf("infra up command missing: %v", err)
	}
	if got := infraUpCmd.Flags().Lookup("provision").DefValue; got != "false" {
		t.Fatalf("infra up default provision = %q, want false", got)
	}
}

func TestBootstrapPrintPlanShowsMissingPrerequisites(t *testing.T) {
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
		"GITHUB_TOKEN",
		"RASCAL_GITHUB_TOKEN",
		"RASCAL_GITHUB_WEBHOOK_SECRET",
		"HCLOUD_TOKEN",
	} {
		t.Setenv(k, "")
	}

	missingAuthPath := filepath.Join(tmp, "missing-auth.json")
	cmd := a.newBootstrapCmd()
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
		t.Fatalf("bootstrap --print-plan failed: %v", err)
	}

	var out bootstrapPlanOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out.Status != "bootstrap_plan" {
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

func TestBootstrapPrintPlanReadyForProvisionFlow(t *testing.T) {
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
	cmd := a.newBootstrapCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--print-plan",
		"--repo", "owner/repo",
		"--provision-new",
		"--hcloud-token", "hcloud-token",
		"--github-admin-token", "admin-token",
		"--github-runtime-token", "runtime-token",
		"--codex-auth", authPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("bootstrap --print-plan failed: %v", err)
	}

	var out bootstrapPlanOutput
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

func TestBootstrapPrintPlanUsesCanonicalRuntimeTokenEnv(t *testing.T) {
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

	cmd := a.newBootstrapCmd()
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
		t.Fatalf("bootstrap --print-plan failed: %v", err)
	}

	var out bootstrapPlanOutput
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

func TestBootstrapPrintPlanIgnoresLegacyRuntimeTokenEnv(t *testing.T) {
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
	t.Setenv("GITHUB_RUNTIME_TOKEN", "legacy-runtime-token")

	cmd := a.newBootstrapCmd()
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
		t.Fatalf("bootstrap --print-plan failed: %v", err)
	}

	var out bootstrapPlanOutput
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

func TestBootstrapStillValidatesWithoutPrintPlan(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}
	for _, k := range []string{
		"GITHUB_ADMIN_TOKEN",
		"GITHUB_TOKEN",
		"RASCAL_GITHUB_TOKEN",
	} {
		t.Setenv(k, "")
	}

	cmd := a.newBootstrapCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--host", "203.0.113.10",
		"--repo", "owner/repo",
		"--github-runtime-token", "runtime-token",
	})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected bootstrap to fail when webhook admin token is missing")
	}
	if !strings.Contains(err.Error(), "--github-admin-token is required") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestBootstrapCompletionJSONOutput(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
		output:     "json",
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}

	cmd := a.newBootstrapCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--skip-deploy",
		"--skip-webhook",
		"--server-url", "https://rascal.example.com",
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("bootstrap: %v", err)
	}

	var out bootstrapCompleteOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out.Status != "bootstrap_complete" {
		t.Fatalf("status = %q, want bootstrap_complete", out.Status)
	}
	if out.ServerURL != "https://rascal.example.com" {
		t.Fatalf("server_url = %q, want https://rascal.example.com", out.ServerURL)
	}
	if out.Host != "" {
		t.Fatalf("host = %q, want empty", out.Host)
	}
	if out.APIToken == "" || !strings.Contains(out.APIToken, "*") {
		t.Fatalf("expected masked api token, got %q", out.APIToken)
	}
	if out.WebhookSecret == "" || !strings.Contains(out.WebhookSecret, "*") {
		t.Fatalf("expected masked webhook secret, got %q", out.WebhookSecret)
	}
}

func TestInfraUpValidatesRequiredInputs(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:8080",
		},
	}
	cmd := a.newInfraUpCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	cmd.SetArgs([]string{})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected infra up to fail without host/provision")
	}
	if !strings.Contains(err.Error(), "--host is required unless --provision is set") {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd = a.newInfraUpCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--host", "203.0.113.10", "--provision"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected infra up to fail when host and provision are combined")
	}
	if !strings.Contains(err.Error(), "--host cannot be combined with --provision") {
		t.Fatalf("unexpected error: %v", err)
	}

	cmd = a.newInfraUpCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	t.Setenv("HCLOUD_TOKEN", "")
	cmd.SetArgs([]string{"--provision"})
	err = cmd.Execute()
	if err == nil {
		t.Fatal("expected infra up to fail without hcloud token in provision mode")
	}
	if !strings.Contains(err.Error(), "missing Hetzner token") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunRetryDebugDefaults(t *testing.T) {
	root := newRootCmd()

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
	root := newRootCmd()

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

	cmd := a.newRunCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--repo", "owner/repo", "--task", "Fix flaky tests"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("run --repo --task: %v", err)
	}

	if payload.Repo != "owner/repo" {
		t.Fatalf("unexpected repo payload: %v", payload.Repo)
	}
	if payload.Task != "Fix flaky tests" {
		t.Fatalf("unexpected task payload: %v", payload.Task)
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

	cmd := a.newRunCmd()
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

	cmd := a.newRunCmd()
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
				ID: "run_old", TaskID: "task_1", Repo: "owner/repo", Task: "Fix it", BaseBranch: "main", Status: state.StatusCanceled,
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
				ID: "run_old", TaskID: "task_1", Repo: "owner/repo", Task: "Fix it", BaseBranch: "main", Status: state.StatusCanceled,
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
				ID: "run_old", TaskID: "owner/repo#123", Repo: "owner/repo", Task: "Fix failing tests", BaseBranch: "main", Status: state.StatusFailed,
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
}

func TestRunIssueInvalidFormat(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			APIToken: "test-token",
		},
	}
	cmd := a.newRunCmd()
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
	cmd := a.newRunCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"--issue", "owner/repo#42", "--task", "Fix it"})
	err := cmd.Execute()
	if err == nil {
		t.Fatal("expected error for mutually exclusive flags")
	}
	if !strings.Contains(err.Error(), "--issue cannot be combined") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestAuthHelpContainsSync(t *testing.T) {
	root := newRootCmd()
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

	root := newRootCmd()
	root.SetArgs([]string{"--config", cfgPath, "config", "set", "server_url", "https://example.com"})
	if err := root.Execute(); err != nil {
		t.Fatalf("config set: %v", err)
	}
	if settings := readClientConfigFileForTest(t, cfgPath); settings.ServerURL == nil {
		t.Fatal("expected server_url to exist in config file after set")
	}

	stdout, err := captureStdout(func() error {
		root := newRootCmd()
		root.SetArgs([]string{"--config", cfgPath, "--output", "json", "config", "unset", "server_url"})
		return root.Execute()
	})
	if err != nil {
		t.Fatalf("config unset: %v", err)
	}

	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if out["status"] != "removed" {
		t.Fatalf("expected status removed, got %v", out["status"])
	}
	if out["source"] != "default" {
		t.Fatalf("expected source default, got %v", out["source"])
	}
	if out["value"] != "http://127.0.0.1:8080" {
		t.Fatalf("expected default server_url, got %v", out["value"])
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
			root := newRootCmd()
			root.SetArgs([]string{"--config", cfgPath, "--output", "json", "config", "unset", "default_repo"})
			return root.Execute()
		})
		if err != nil {
			t.Fatalf("config unset attempt %d: %v", i+1, err)
		}
		var out map[string]any
		if err := json.Unmarshal([]byte(stdout), &out); err != nil {
			t.Fatalf("invalid json output attempt %d: %v", i+1, err)
		}
		if out["status"] != "absent" {
			t.Fatalf("expected status absent attempt %d, got %v", i+1, out["status"])
		}
		if msg, ok := out["message"].(string); !ok || strings.TrimSpace(msg) == "" {
			t.Fatalf("expected message to be set attempt %d, got %v", i+1, out["message"])
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
		root := newRootCmd()
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

func TestConfigSetSSHPortPersistsInteger(t *testing.T) {
	tmp := t.TempDir()
	cfgPath := filepath.Join(tmp, "config.toml")

	root := newRootCmd()
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
		{name: "bootstrap", args: []string{"bootstrap"}},
		{name: "deploy", args: []string{"deploy"}},
		{name: "auth", args: []string{"auth"}},
		{name: "auth_credentials", args: []string{"auth", "credentials"}},
		{name: "github", args: []string{"github"}},
		{name: "infra", args: []string{"infra"}},
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

func TestNoColorRequested(t *testing.T) {
	t.Setenv("NO_COLOR", "1")
	if !noColorRequested(false) {
		t.Fatal("expected NO_COLOR env to disable color/styling")
	}
	if !noColorRequested(true) {
		t.Fatal("expected --no-color flag to disable color/styling")
	}
}

func TestNoColorRequestedUnset(t *testing.T) {
	old, had := os.LookupEnv("NO_COLOR")
	if err := os.Unsetenv("NO_COLOR"); err != nil {
		t.Fatalf("unset NO_COLOR: %v", err)
	}
	t.Cleanup(func() {
		if had {
			if err := os.Setenv("NO_COLOR", old); err != nil {
				t.Errorf("restore NO_COLOR: %v", err)
			}
			return
		}
		if err := os.Unsetenv("NO_COLOR"); err != nil {
			t.Errorf("unset NO_COLOR: %v", err)
		}
	})
	if noColorRequested(false) {
		t.Fatal("expected color/styling enabled when NO_COLOR is unset and flag is false")
	}
}

func TestValidateDistinctGitHubTokens(t *testing.T) {
	if err := validateDistinctGitHubTokens("admin", "runtime", true); err != nil {
		t.Fatalf("expected distinct tokens to pass: %v", err)
	}
	if err := validateDistinctGitHubTokens("same", "same", true); err == nil {
		t.Fatal("expected equal tokens to fail when strict separation is enforced")
	}
	if err := validateDistinctGitHubTokens("same", "same", false); err != nil {
		t.Fatalf("expected no error when strict separation is disabled: %v", err)
	}
}

func TestGoarchFromUnameMachine(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "x86_64", want: "amd64", ok: true},
		{input: "amd64", want: "amd64", ok: true},
		{input: "aarch64", want: "arm64", ok: true},
		{input: "arm64", want: "arm64", ok: true},
		{input: "mips64", want: "", ok: false},
	}

	for _, tc := range cases {
		got, ok := goarchFromUnameMachine(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("goarchFromUnameMachine(%q) = (%q, %t), want (%q, %t)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGoarchFromHetznerArchitecture(t *testing.T) {
	t.Parallel()

	cases := []struct {
		input string
		want  string
		ok    bool
	}{
		{input: "x86", want: "amd64", ok: true},
		{input: "amd64", want: "amd64", ok: true},
		{input: "arm", want: "arm64", ok: true},
		{input: "arm64", want: "arm64", ok: true},
		{input: "sparc", want: "", ok: false},
	}

	for _, tc := range cases {
		got, ok := goarchFromHetznerArchitecture(tc.input)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("goarchFromHetznerArchitecture(%q) = (%q, %t), want (%q, %t)", tc.input, got, ok, tc.want, tc.ok)
		}
	}
}

func TestResolveRepoPrecedence(t *testing.T) {
	t.Parallel()

	t.Run("explicit wins", func(t *testing.T) {
		t.Parallel()
		called := false
		repo, inferred := resolveRepo("explicit/repo", "default/repo", func() string {
			called = true
			return "git/repo"
		})
		if called {
			t.Fatal("expected infer not called for explicit repo")
		}
		if inferred {
			t.Fatal("expected inferred=false for explicit repo")
		}
		if repo != "explicit/repo" {
			t.Fatalf("unexpected repo: %q", repo)
		}
	})

	t.Run("default wins", func(t *testing.T) {
		t.Parallel()
		called := false
		repo, inferred := resolveRepo("", "default/repo", func() string {
			called = true
			return "git/repo"
		})
		if called {
			t.Fatal("expected infer not called for default repo")
		}
		if inferred {
			t.Fatal("expected inferred=false for default repo")
		}
		if repo != "default/repo" {
			t.Fatalf("unexpected repo: %q", repo)
		}
	})

	t.Run("git inference used", func(t *testing.T) {
		t.Parallel()
		called := false
		repo, inferred := resolveRepo("", "", func() string {
			called = true
			return "git/repo"
		})
		if !called {
			t.Fatal("expected infer called when explicit/default missing")
		}
		if !inferred {
			t.Fatal("expected inferred=true for git repo")
		}
		if repo != "git/repo" {
			t.Fatalf("unexpected repo: %q", repo)
		}
	})

	t.Run("empty inference ignored", func(t *testing.T) {
		t.Parallel()
		repo, inferred := resolveRepo("", "", func() string { return "" })
		if inferred {
			t.Fatal("expected inferred=false when inference is empty")
		}
		if repo != "" {
			t.Fatalf("expected empty repo, got %q", repo)
		}
	})
}

func TestParseGitHubRepoFromRemote(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		input  string
		want   string
		expect bool
	}{
		{name: "ssh git suffix", input: "git@github.com:owner/repo.git", want: "owner/repo", expect: true},
		{name: "ssh no suffix", input: "git@github.com:owner/repo", want: "owner/repo", expect: true},
		{name: "https git suffix", input: "https://github.com/owner/repo.git", want: "owner/repo", expect: true},
		{name: "https no suffix", input: "https://github.com/owner/repo", want: "owner/repo", expect: true},
		{name: "https trailing slash", input: "https://github.com/owner/repo/", want: "owner/repo", expect: true},
		{name: "non github", input: "https://gitlab.com/owner/repo.git", want: "", expect: false},
		{name: "missing repo", input: "git@github.com:owner", want: "", expect: false},
		{name: "extra path", input: "git@github.com:owner/repo/extra", want: "", expect: false},
		{name: "empty", input: "", want: "", expect: false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := parseGitHubRepoFromRemote(tc.input)
			if ok != tc.expect {
				t.Fatalf("parseGitHubRepoFromRemote(%q) ok=%t want %t", tc.input, ok, tc.expect)
			}
			if got != tc.want {
				t.Fatalf("parseGitHubRepoFromRemote(%q) = %q want %q", tc.input, got, tc.want)
			}
		})
	}
}

func TestRascaldJournalctlRemoteCmd(t *testing.T) {
	t.Parallel()

	cmd := rascaldJournalctlRemoteCmd(200, false)
	if strings.Contains(cmd, ";;;") {
		t.Fatalf("remote command contains malformed case terminator: %q", cmd)
	}
	if !strings.Contains(cmd, "fi\n    ;;") {
		t.Fatalf("expected case fallback branch to terminate cleanly after fi: %q", cmd)
	}
	if !strings.Contains(cmd, "journalctl -u \"$unit\" --no-pager -n 200") {
		t.Fatalf("expected journalctl line count in remote command: %q", cmd)
	}
	if strings.Contains(cmd, "journalctl -u \"$unit\" --no-pager -n 200 -f") {
		t.Fatalf("unexpected follow flag in non-follow command: %q", cmd)
	}

	followCmd := rascaldJournalctlRemoteCmd(50, true)
	if !strings.Contains(followCmd, "journalctl -u \"$unit\" --no-pager -n 50 -f") {
		t.Fatalf("expected follow journalctl command, got: %q", followCmd)
	}

	defaultLinesCmd := rascaldJournalctlRemoteCmd(0, false)
	if !strings.Contains(defaultLinesCmd, "journalctl -u \"$unit\" --no-pager -n 200") {
		t.Fatalf("expected default line count when lines<=0, got: %q", defaultLinesCmd)
	}
}
func TestResolveTransport(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name       string
		configured string
		serverURL  string
		sshHost    string
		want       string
	}{
		{
			name:       "explicit http",
			configured: "http",
			serverURL:  "http://127.0.0.1:8080",
			sshHost:    "203.0.113.10",
			want:       "http",
		},
		{
			name:       "explicit ssh",
			configured: "ssh",
			serverURL:  "https://rascal.example.com",
			sshHost:    "203.0.113.10",
			want:       "ssh",
		},
		{
			name:       "auto localhost prefers ssh",
			configured: "auto",
			serverURL:  "http://127.0.0.1:8080",
			sshHost:    "203.0.113.10",
			want:       "ssh",
		},
		{
			name:       "auto remote 8080 prefers ssh",
			configured: "auto",
			serverURL:  "http://203.0.113.10:8080",
			sshHost:    "203.0.113.10",
			want:       "ssh",
		},
		{
			name:       "auto https prefers http",
			configured: "auto",
			serverURL:  "https://rascal.example.com",
			sshHost:    "203.0.113.10",
			want:       "http",
		},
		{
			name:       "auto without ssh host is http",
			configured: "auto",
			serverURL:  "http://127.0.0.1:8080",
			sshHost:    "",
			want:       "http",
		},
	}

	for _, tc := range cases {
		got := resolveTransport(tc.configured, tc.serverURL, tc.sshHost)
		if got != tc.want {
			t.Fatalf("%s: resolveTransport(%q, %q, %q) = %q, want %q", tc.name, tc.configured, tc.serverURL, tc.sshHost, got, tc.want)
		}
	}
}

func TestLoadEnvFile(t *testing.T) {
	path := t.TempDir() + "/.env"
	content := `
# comment
HCLOUD_TOKEN=test_hcloud_token
export GITHUB_ADMIN_TOKEN=test_github_admin_token
	RASCAL_GITHUB_TOKEN="test_github_runtime_token"
EMPTY=
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	got, err := loadEnvFile(path)
	if err != nil {
		t.Fatalf("load env file: %v", err)
	}
	if got["HCLOUD_TOKEN"] != "test_hcloud_token" {
		t.Fatalf("unexpected HCLOUD_TOKEN: %q", got["HCLOUD_TOKEN"])
	}
	if got["GITHUB_ADMIN_TOKEN"] != "test_github_admin_token" {
		t.Fatalf("unexpected GITHUB_ADMIN_TOKEN: %q", got["GITHUB_ADMIN_TOKEN"])
	}
	if got["RASCAL_GITHUB_TOKEN"] != "test_github_runtime_token" {
		t.Fatalf("unexpected RASCAL_GITHUB_TOKEN: %q", got["RASCAL_GITHUB_TOKEN"])
	}
}

func TestLoadEnvFileInvalidLine(t *testing.T) {
	path := t.TempDir() + "/.env"
	if err := os.WriteFile(path, []byte("NOT_VALID\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	if _, err := loadEnvFile(path); err == nil {
		t.Fatal("expected parse error for invalid env line")
	}
}

func TestLoadGlobalEnvDefaultDotRascalEnv(t *testing.T) {
	tmp := t.TempDir()
	oldWD, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(oldWD); err != nil {
			t.Errorf("restore wd: %v", err)
		}
	})
	if err := os.Chdir(tmp); err != nil {
		t.Fatalf("chdir: %v", err)
	}

	if err := os.WriteFile(filepath.Join(tmp, ".rascal.env"), []byte("RASCAL_TEST_ENV_FALLBACK=from_file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("RASCAL_TEST_ENV_FALLBACK", "")

	a := &app{}
	if err := a.loadGlobalEnv(); err != nil {
		t.Fatalf("loadGlobalEnv: %v", err)
	}
	if got := os.Getenv("RASCAL_TEST_ENV_FALLBACK"); got != "from_file" {
		t.Fatalf("unexpected env value: %q", got)
	}
}

func TestLoadGlobalEnvExplicitEnvWinsOverFile(t *testing.T) {
	tmp := t.TempDir()
	envPath := filepath.Join(tmp, "x.env")
	if err := os.WriteFile(envPath, []byte("RASCAL_TEST_ENV_PRIORITY=from_file\n"), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	t.Setenv("RASCAL_TEST_ENV_PRIORITY", "from_env")

	a := &app{envFilePath: envPath}
	if err := a.loadGlobalEnv(); err != nil {
		t.Fatalf("loadGlobalEnv: %v", err)
	}
	if got := os.Getenv("RASCAL_TEST_ENV_PRIORITY"); got != "from_env" {
		t.Fatalf("expected env var to keep precedence, got %q", got)
	}
}

func TestStreamRunLogsFollowAppendsOnlyDiff(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "alpha\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "alpha\nbeta\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "alpha\nbeta\ngamma\n", RunStatus: state.StatusSucceeded, Done: true},
	}

	a, closeServer, _ := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_abc123", true, 1*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unexpected follow output:\n--- got ---\n%s\n--- want ---\n%s", out, "alpha\nbeta\ngamma\n")
	}
}

func TestStreamRunLogsFollowPrintsFullBodyOnReset(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "one\ntwo\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "reset\n", RunStatus: state.StatusRunning, Done: true},
	}

	a, closeServer, _ := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_reset", true, 1*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "one\ntwo\nreset\n" {
		t.Fatalf("unexpected follow output on reset:\n--- got ---\n%s\n--- want ---\n%s", out, "one\ntwo\nreset\n")
	}
}

func TestStreamRunLogsFollowStopsAfterDone(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "done-now\n", RunStatus: state.StatusFailed, Done: true},
	}

	a, closeServer, requestCount := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_done", true, 5*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "done-now\n" {
		t.Fatalf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", out, "done-now\n")
	}
	if got := requestCount(); got != 1 {
		t.Fatalf("expected exactly one follow request when done=true, got %d", got)
	}
}

func newFollowLogsTestApp(t *testing.T, responses []api.RunLogsResponse) (*app, func(), func() int) {
	t.Helper()

	var (
		mu    sync.Mutex
		idx   int
		calls int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if !strings.HasPrefix(r.URL.Path, "/v1/runs/") || !strings.HasSuffix(r.URL.Path, "/logs") {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("format"); got != "json" {
			t.Fatalf("expected format=json, got %q", got)
		}

		mu.Lock()
		defer mu.Unlock()
		calls++
		current := responses[len(responses)-1]
		if idx < len(responses) {
			current = responses[idx]
			idx++
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(current); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))

	a := &app{
		cfg: config.ClientConfig{
			ServerURL:   srv.URL,
			APIToken:    "test-token",
			Transport:   "http",
			SSHPort:     22,
			DefaultRepo: "owner/repo",
		},
		client: apiClient{
			baseURL:   srv.URL,
			token:     "test-token",
			http:      srv.Client(),
			transport: "http",
		},
	}

	getCalls := func() int {
		mu.Lock()
		defer mu.Unlock()
		return calls
	}
	return a, srv.Close, getCalls
}

func captureStdout(fn func() error) (string, error) {
	old := os.Stdout
	r, w, err := os.Pipe()
	if err != nil {
		return "", fmt.Errorf("create stdout pipe: %w", err)
	}
	os.Stdout = w

	err = fn()
	if closeErr := w.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	os.Stdout = old

	data, readErr := io.ReadAll(r)
	if readErr != nil && err == nil {
		err = readErr
	}
	if closeErr := r.Close(); err == nil && closeErr != nil {
		err = closeErr
	}
	return string(data), err
}

func renderHelpOutput(t *testing.T, args ...string) string {
	t.Helper()
	root := newRootCmd()
	var stdout bytes.Buffer
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	allArgs := append([]string{}, args...)
	allArgs = append(allArgs, "--help")
	root.SetArgs(allArgs)
	if err := root.Execute(); err != nil {
		t.Fatalf("execute help for %v: %v", args, err)
	}
	return normalizeHelpOutput(stdout.String())
}

func normalizeHelpOutput(s string) string {
	s = strings.ReplaceAll(s, "\r\n", "\n")
	if home, err := os.UserHomeDir(); err == nil && home != "" {
		s = strings.ReplaceAll(s, home, "$HOME")
	}
	return strings.TrimSpace(s) + "\n"
}

func containsSubstring(items []string, needle string) bool {
	for _, item := range items {
		if strings.Contains(item, needle) {
			return true
		}
	}
	return false
}

func containsExact(items []string, needle string) bool {
	for _, item := range items {
		if item == needle {
			return true
		}
	}
	return false
}

func readClientConfigFileForTest(t *testing.T, path string) clientConfigFile {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config file: %v", err)
	}
	var out clientConfigFile
	if err := toml.Unmarshal(data, &out); err != nil {
		t.Fatalf("parse config file: %v", err)
	}
	return out
}

func clearEnvKeys(t *testing.T, keys ...string) {
	t.Helper()
	for _, key := range keys {
		key := key
		val, had := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if had {
				if err := os.Setenv(key, val); err != nil {
					t.Errorf("restore %s: %v", key, err)
				}
				return
			}
			if err := os.Unsetenv(key); err != nil {
				t.Errorf("unset %s: %v", key, err)
			}
		})
	}
}
