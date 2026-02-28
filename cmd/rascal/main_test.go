package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"os"
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
