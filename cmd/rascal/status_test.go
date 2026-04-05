package main

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
)

func newStatusTestApp(t *testing.T, handler http.Handler) *app {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	return &app{
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
}

func serveSystemStatus(t *testing.T, out api.SystemStatusResponse) http.Handler {
	t.Helper()
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/status" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(out); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	})
}

func TestStatusReadyWithCredentials(t *testing.T) {
	a := newStatusTestApp(t, serveSystemStatus(t, api.SystemStatusResponse{
		Ready:             true,
		ActiveCredentials: 2,
	}))

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("status execute: %v", err)
	}
	if !strings.Contains(stdout, "daemon") || !strings.Contains(stdout, "ok") {
		t.Fatalf("expected daemon ok in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "ready") || !strings.Contains(stdout, "yes") {
		t.Fatalf("expected ready yes in output, got:\n%s", stdout)
	}
	if !strings.Contains(stdout, "2 active credentials") {
		t.Fatalf("expected 2 active credentials in output, got:\n%s", stdout)
	}
}

func TestStatusReadyWithSingleCredential(t *testing.T) {
	a := newStatusTestApp(t, serveSystemStatus(t, api.SystemStatusResponse{
		Ready:             true,
		ActiveCredentials: 1,
	}))

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("status execute: %v", err)
	}
	if !strings.Contains(stdout, "1 active credential") {
		t.Fatalf("expected singular credential label in output, got:\n%s", stdout)
	}
	if strings.Contains(stdout, "1 active credentials") {
		t.Fatalf("unexpected plural label in output, got:\n%s", stdout)
	}
}

func TestStatusReadyNoCredentials(t *testing.T) {
	a := newStatusTestApp(t, serveSystemStatus(t, api.SystemStatusResponse{
		Ready:             true,
		ActiveCredentials: 0,
	}))

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("status execute: %v", err)
	}
	if !strings.Contains(stdout, "none") || !strings.Contains(stdout, "no credentials configured") {
		t.Fatalf("expected none / no credentials configured in output, got:\n%s", stdout)
	}
}

func TestStatusNotReady(t *testing.T) {
	a := newStatusTestApp(t, serveSystemStatus(t, api.SystemStatusResponse{
		Ready:             false,
		ActiveCredentials: 3,
	}))

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err == nil {
		t.Fatal("expected non-zero exit when daemon not ready")
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("expected cliError, got %T: %v", err, err)
	}
	if ce.Code != exitRuntime {
		t.Fatalf("exit code = %d, want exitRuntime (%d)", ce.Code, exitRuntime)
	}
	if !strings.Contains(stdout, "draining") {
		t.Fatalf("expected draining in table output, got:\n%s", stdout)
	}
}

func TestStatusJSONOutput(t *testing.T) {
	a := newStatusTestApp(t, serveSystemStatus(t, api.SystemStatusResponse{
		Ready:             true,
		ActiveCredentials: 2,
	}))
	a.output = "json"

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("status execute: %v", err)
	}
	var out api.SystemStatusResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode JSON output: %v\noutput:\n%s", err, stdout)
	}
	if !out.Ready {
		t.Fatalf("expected ready=true, got false")
	}
	if out.ActiveCredentials != 2 {
		t.Fatalf("active_credentials = %d, want 2", out.ActiveCredentials)
	}
}

func TestStatusUnreachable(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://127.0.0.1:1",
			APIToken:  "test-token",
			Transport: "http",
		},
		client: apiClient{
			baseURL:   "http://127.0.0.1:1",
			token:     "test-token",
			transport: "http",
		},
		output: "table",
	}

	cmd := a.newStatusCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)

	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err == nil {
		t.Fatal("expected error when daemon unreachable")
	}
	var ce *cliError
	if !errors.As(err, &ce) {
		t.Fatalf("expected cliError, got %T: %v", err, err)
	}
	if ce.Code != exitServer {
		t.Fatalf("exit code = %d, want exitServer (%d)", ce.Code, exitServer)
	}
	if !strings.Contains(stdout, "unreachable") {
		t.Fatalf("expected unreachable in table output, got:\n%s", stdout)
	}
}

func TestStatusCommandRegistered(t *testing.T) {
	root := mustNewRootCmd(t)
	if _, _, err := root.Find([]string{"status"}); err != nil {
		t.Fatalf("status command missing: %v", err)
	}
}
