package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
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
}

func TestRootHasRepoAndInfraCommands(t *testing.T) {
	root := newRootCmd()
	if _, _, err := root.Find([]string{"repo"}); err != nil {
		t.Fatalf("repo command missing: %v", err)
	}
	if _, _, err := root.Find([]string{"infra"}); err != nil {
		t.Fatalf("infra command missing: %v", err)
	}
}

func TestBootstrapAndInfraDefaults(t *testing.T) {
	root := newRootCmd()

	bootstrapCmd, _, err := root.Find([]string{"bootstrap"})
	if err != nil {
		t.Fatalf("bootstrap command missing: %v", err)
	}
	if got := bootstrapCmd.Flags().Lookup("hcloud-server-type").DefValue; got != "cx23" {
		t.Fatalf("bootstrap default hcloud server type = %q, want cx23", got)
	}
	if got := bootstrapCmd.Flags().Lookup("goarch").DefValue; got != "" {
		t.Fatalf("bootstrap default goarch = %q, want empty for auto-detect", got)
	}

	provisionCmd, _, err := root.Find([]string{"infra", "provision-hetzner"})
	if err != nil {
		t.Fatalf("infra provision-hetzner command missing: %v", err)
	}
	if got := provisionCmd.Flags().Lookup("server-type").DefValue; got != "cx23" {
		t.Fatalf("infra provision default server type = %q, want cx23", got)
	}

	deployCmd, _, err := root.Find([]string{"infra", "deploy-existing"})
	if err != nil {
		t.Fatalf("infra deploy-existing command missing: %v", err)
	}
	if got := deployCmd.Flags().Lookup("goarch").DefValue; got != "" {
		t.Fatalf("infra deploy-existing default goarch = %q, want empty for auto-detect", got)
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

func TestLoadEnvFile(t *testing.T) {
	path := t.TempDir() + "/.env"
	content := `
# comment
HCLOUD_TOKEN=abc
export GITHUB_ADMIN_TOKEN=admin_token
GITHUB_RUNTIME_TOKEN="runtime_token"
EMPTY=
`
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write env file: %v", err)
	}
	got, err := loadEnvFile(path)
	if err != nil {
		t.Fatalf("load env file: %v", err)
	}
	if got["HCLOUD_TOKEN"] != "abc" {
		t.Fatalf("unexpected HCLOUD_TOKEN: %q", got["HCLOUD_TOKEN"])
	}
	if got["GITHUB_ADMIN_TOKEN"] != "admin_token" {
		t.Fatalf("unexpected GITHUB_ADMIN_TOKEN: %q", got["GITHUB_ADMIN_TOKEN"])
	}
	if got["GITHUB_RUNTIME_TOKEN"] != "runtime_token" {
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
