package main

import (
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
	type emitOutput struct {
		OK bool `json:"ok"`
	}
	stdout, err := captureStdout(func() error {
		return emit(a, emitOutput{OK: true}, nil)
	})
	if err != nil {
		t.Fatalf("emit: %v", err)
	}
	var out emitOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("invalid json output: %v", err)
	}
	if !out.OK {
		t.Fatalf("unexpected output: %v", out)
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
		{name: "explicit http", configured: "http", serverURL: "http://127.0.0.1:8080", sshHost: "203.0.113.10", want: "http"},
		{name: "explicit ssh", configured: "ssh", serverURL: "https://rascal.example.com", sshHost: "203.0.113.10", want: "ssh"},
		{name: "auto localhost prefers ssh", configured: "auto", serverURL: "http://127.0.0.1:8080", sshHost: "203.0.113.10", want: "ssh"},
		{name: "auto remote 8080 prefers ssh", configured: "auto", serverURL: "http://203.0.113.10:8080", sshHost: "203.0.113.10", want: "ssh"},
		{name: "auto https prefers http", configured: "auto", serverURL: "https://rascal.example.com", sshHost: "203.0.113.10", want: "http"},
		{name: "auto without ssh host is http", configured: "auto", serverURL: "http://127.0.0.1:8080", sshHost: "", want: "http"},
	}

	for _, tc := range cases {
		got := resolveTransport(tc.configured, tc.serverURL, tc.sshHost)
		if got != tc.want {
			t.Fatalf("%s: resolveTransport(%q, %q, %q) = %q, want %q", tc.name, tc.configured, tc.serverURL, tc.sshHost, got, tc.want)
		}
	}
}

func TestLoadEnvFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".env")
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
	path := filepath.Join(t.TempDir(), ".env")
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
