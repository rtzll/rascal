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
	"sync"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
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
		return a.emit(map[string]any{"ok": true}, nil)
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

func TestRootHasRepoAndInfraCommands(t *testing.T) {
	root := newRootCmd()
	if _, _, err := root.Find([]string{"deploy"}); err != nil {
		t.Fatalf("deploy command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"repo"}); err != nil {
		t.Fatalf("repo command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"github"}); err != nil {
		t.Fatalf("github command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"github", "setup"}); err != nil {
		t.Fatalf("github setup command missing: %v", err)
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
	if got := deployCmd.Flags().Lookup("runner-image").DefValue; got != "rascal-runner:latest" {
		t.Fatalf("deploy default runner-image = %q, want rascal-runner:latest", got)
	}
	if got := deployCmd.Flags().Lookup("skip-env-upload").DefValue; got != "false" {
		t.Fatalf("deploy default skip-env-upload = %q, want false", got)
	}
	if got := deployCmd.Flags().Lookup("skip-auth-upload").DefValue; got != "false" {
		t.Fatalf("deploy default skip-auth-upload = %q, want false", got)
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
	if got := infraDeployCmd.Flags().Lookup("runner-image").DefValue; got != "rascal-runner:latest" {
		t.Fatalf("infra deploy-existing default runner-image = %q, want rascal-runner:latest", got)
	}
	if got := infraDeployCmd.Flags().Lookup("skip-env-upload").DefValue; got != "false" {
		t.Fatalf("infra deploy-existing default skip-env-upload = %q, want false", got)
	}
	if got := infraDeployCmd.Flags().Lookup("skip-auth-upload").DefValue; got != "false" {
		t.Fatalf("infra deploy-existing default skip-auth-upload = %q, want false", got)
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
		"GITHUB_RUNTIME_TOKEN",
		"RASCAL_GITHUB_RUNTIME_TOKEN",
		"RASCAL_GITHUB_WEBHOOK_SECRET",
		"GITHUB_WEBHOOK_SECRET",
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

	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if out["status"] != "bootstrap_plan" {
		t.Fatalf("unexpected status: %v", out["status"])
	}
	if ready, ok := out["ready"].(bool); !ok || ready {
		t.Fatalf("expected ready=false, got %v", out["ready"])
	}
	missing := anySliceToStrings(out["missing"])
	if !containsSubstring(missing, "GitHub admin token is required") {
		t.Fatalf("missing list should include admin token requirement, got %v", missing)
	}
	if !containsSubstring(missing, "GitHub runtime token is required") {
		t.Fatalf("missing list should include runtime token requirement, got %v", missing)
	}
	if !containsSubstring(missing, "codex auth file not found") {
		t.Fatalf("missing list should include codex auth file check, got %v", missing)
	}

	actions := anySliceToStrings(out["actions"])
	if !containsExact(actions, "deploy rascald to 203.0.113.10 over SSH") {
		t.Fatalf("actions should include deploy step, got %v", actions)
	}
	if !containsExact(actions, "configure GitHub webhook and label for owner/repo") {
		t.Fatalf("actions should include webhook step, got %v", actions)
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

	var out map[string]any
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode json output: %v\noutput:\n%s", err, stdout)
	}
	if ready, ok := out["ready"].(bool); !ok || !ready {
		t.Fatalf("expected ready=true, got %v", out["ready"])
	}
	missing := anySliceToStrings(out["missing"])
	if len(missing) != 0 {
		t.Fatalf("expected no missing prerequisites, got %v", missing)
	}

	resolved, ok := out["resolved"].(map[string]any)
	if !ok {
		t.Fatalf("resolved section missing: %v", out["resolved"])
	}
	if got := resolved["server_url"]; got != "http://<provisioned-host>:8080" {
		t.Fatalf("unexpected server_url: %v", got)
	}
	if got := resolved["server_url_source"]; got != "provisioned_host" {
		t.Fatalf("unexpected server_url_source: %v", got)
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
		"GITHUB_RUNTIME_TOKEN",
		"RASCAL_GITHUB_RUNTIME_TOKEN",
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

func TestRunIssueCreatesIssueRunPayload(t *testing.T) {
	var payload map[string]any
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
		_ = json.NewEncoder(w).Encode(map[string]any{
			"run": map[string]any{
				"id":     "run_issue",
				"status": "queued",
			},
		})
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

	if payload["repo"] != "owner/repo" {
		t.Fatalf("unexpected repo payload: %v", payload["repo"])
	}
	if payload["issue_number"] != float64(123) {
		t.Fatalf("unexpected issue number payload: %v", payload["issue_number"])
	}
	if payload["debug"] != true {
		t.Fatalf("unexpected debug payload: %v", payload["debug"])
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
		{name: "github", args: []string{"github"}},
		{name: "repo", args: []string{"repo"}},
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
			_ = os.Setenv("NO_COLOR", old)
			return
		}
		_ = os.Unsetenv("NO_COLOR")
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
GITHUB_RUNTIME_TOKEN="test_github_runtime_token"
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
	if got["GITHUB_RUNTIME_TOKEN"] != "test_github_runtime_token" {
		t.Fatalf("unexpected GITHUB_RUNTIME_TOKEN: %q", got["GITHUB_RUNTIME_TOKEN"])
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
	t.Cleanup(func() { _ = os.Chdir(oldWD) })
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
	responses := []map[string]any{
		{"logs": "alpha\n", "run_status": "running", "done": false},
		{"logs": "alpha\nbeta\n", "run_status": "running", "done": false},
		{"logs": "alpha\nbeta\ngamma\n", "run_status": "succeeded", "done": true},
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
	responses := []map[string]any{
		{"logs": "one\ntwo\n", "run_status": "running", "done": false},
		{"logs": "reset\n", "run_status": "running", "done": true},
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
	responses := []map[string]any{
		{"logs": "done-now\n", "run_status": "failed", "done": true},
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

func newFollowLogsTestApp(t *testing.T, responses []map[string]any) (*app, func(), func() int) {
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
		_ = json.NewEncoder(w).Encode(current)
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
	r, w, _ := os.Pipe()
	os.Stdout = w

	err := fn()
	_ = w.Close()
	os.Stdout = old

	data, _ := io.ReadAll(r)
	_ = r.Close()
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

func anySliceToStrings(v any) []string {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if s, ok := item.(string); ok {
			out = append(out, s)
		}
	}
	return out
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
