package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
)

func TestAuthHelpContainsCredentials(t *testing.T) {
	root := newRootCmd()
	var stdout strings.Builder
	root.SetOut(&stdout)
	root.SetErr(&stdout)
	root.SetArgs([]string{"auth", "--help"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v", err)
	}
	if !strings.Contains(stdout.String(), "credentials") {
		t.Fatalf("expected auth help to include credentials command\n%s", stdout.String())
	}
}

func TestAuthCredentialsListJSON(t *testing.T) {
	now := time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/credentials" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credentials": []map[string]any{
				{
					"id":                "cred_one",
					"owner_user_id":     "user_1",
					"scope":             "personal",
					"weight":            2,
					"max_active_leases": 3,
					"status":            "active",
					"created_at":        now,
					"updated_at":        now,
				},
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"credentials", "list"})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("credentials list: %v", err)
	}

	var out credentialListResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if len(out.Credentials) != 1 || out.Credentials[0].ID != "cred_one" {
		t.Fatalf("unexpected credentials output: %+v", out.Credentials)
	}
}

func TestAuthCredentialsCreateUsesAuthFile(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}
	var payload credentialCreateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/credentials" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]any{
				"id":                "cred_create",
				"owner_user_id":     "user_1",
				"scope":             payload.Scope,
				"weight":            payload.Weight,
				"max_active_leases": payload.MaxActiveLeases,
				"status":            "active",
				"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				"updated_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"credentials", "create",
		"--id", "cred_create",
		"--scope", "shared",
		"--owner-user-id", "ignored_owner",
		"--weight", "5",
		"--max-active-leases", "7",
		"--auth-file", authPath,
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("credentials create: %v", err)
	}
	if payload.ID != "cred_create" {
		t.Fatalf("payload id = %q, want cred_create", payload.ID)
	}
	if payload.Scope != "shared" {
		t.Fatalf("payload scope = %q, want shared", payload.Scope)
	}
	if payload.Weight != 5 || payload.MaxActiveLeases != 7 {
		t.Fatalf("unexpected numeric payload: %+v", payload)
	}
	if payload.AuthBlob != `{"token":"abc"}` {
		t.Fatalf("payload auth blob = %q", payload.AuthBlob)
	}
	var out credentialGetResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.Credential.ID != "cred_create" {
		t.Fatalf("unexpected credential output: %+v", out.Credential)
	}
}

func TestAuthCredentialsUpdateSendsChangedFields(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/credentials/cred_update" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]any{
				"id":                "cred_update",
				"owner_user_id":     "user_2",
				"scope":             "personal",
				"weight":            9,
				"max_active_leases": 4,
				"status":            "active",
				"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				"updated_at":        time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"credentials", "update", "cred_update",
		"--scope", "personal",
		"--owner-user-id", "user_2",
		"--weight", "9",
		"--max-active-leases", "4",
		"--auth-blob", `{"token":"updated"}`,
	})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials update: %v", err)
	}
	if raw["scope"] != "personal" {
		t.Fatalf("unexpected scope payload: %v", raw["scope"])
	}
	if raw["owner_user_id"] != "user_2" {
		t.Fatalf("unexpected owner payload: %v", raw["owner_user_id"])
	}
	if raw["weight"] != float64(9) {
		t.Fatalf("unexpected weight payload: %v", raw["weight"])
	}
	if raw["max_active_leases"] != float64(4) {
		t.Fatalf("unexpected max_active_leases payload: %v", raw["max_active_leases"])
	}
	if raw["auth_blob"] != `{"token":"updated"}` {
		t.Fatalf("unexpected auth blob payload: %v", raw["auth_blob"])
	}
}

func TestAuthCredentialsDisableFetchesUpdatedCredential(t *testing.T) {
	var deletes, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/credentials/cred_disable":
			deletes++
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{"disabled": true}); err != nil {
				t.Fatalf("encode delete response: %v", err)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/cred_disable":
			gets++
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(map[string]any{
				"credential": map[string]any{
					"id":                "cred_disable",
					"owner_user_id":     "user_1",
					"scope":             "personal",
					"weight":            1,
					"max_active_leases": 1,
					"status":            "disabled",
					"last_error":        "disabled by API",
					"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
					"updated_at":        time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
				},
			}); err != nil {
				t.Fatalf("encode get response: %v", err)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"credentials", "disable", "cred_disable"})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("credentials disable: %v", err)
	}
	if deletes != 1 || gets != 1 {
		t.Fatalf("expected one delete and one get, got deletes=%d gets=%d", deletes, gets)
	}
	var out credentialDisableResponse
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if !out.Disabled || out.Credential.Status != "disabled" {
		t.Fatalf("unexpected disable output: %+v", out)
	}
}

func TestAuthCredentialsEnableClearsCooldown(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/credentials/cred_enable" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]any{
				"id":                "cred_enable",
				"owner_user_id":     "user_1",
				"scope":             "personal",
				"weight":            1,
				"max_active_leases": 1,
				"status":            "active",
				"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				"updated_at":        time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"credentials", "enable", "cred_enable"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials enable: %v", err)
	}
	if raw["status"] != "active" {
		t.Fatalf("unexpected status payload: %v", raw["status"])
	}
	if raw["cooldown_until"] != "" {
		t.Fatalf("expected cooldown_until clear payload, got %v", raw["cooldown_until"])
	}
	if raw["last_error"] != "" {
		t.Fatalf("expected last_error clear payload, got %v", raw["last_error"])
	}
}

func TestAuthCredentialsCooldownSetsCooldown(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if r.URL.Path != "/v1/credentials/cred_cooldown" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]any{
				"id":                "cred_cooldown",
				"owner_user_id":     "user_1",
				"scope":             "personal",
				"weight":            1,
				"max_active_leases": 1,
				"status":            "cooldown",
				"cooldown_until":    time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC),
				"last_error":        "manual cooldown",
				"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				"updated_at":        time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"credentials", "cooldown", "cred_cooldown", "--for", "30m"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials cooldown: %v", err)
	}
	if raw["status"] != "cooldown" {
		t.Fatalf("unexpected status payload: %v", raw["status"])
	}
	if raw["last_error"] != "manual cooldown" {
		t.Fatalf("unexpected last_error payload: %v", raw["last_error"])
	}
	until, ok := raw["cooldown_until"].(string)
	if !ok || strings.TrimSpace(until) == "" {
		t.Fatalf("expected cooldown_until payload, got %v", raw["cooldown_until"])
	}
	if _, err := time.Parse(time.RFC3339, until); err != nil {
		t.Fatalf("cooldown_until should be RFC3339, got %q (%v)", until, err)
	}
}

func TestAuthCredentialsCooldownClear(t *testing.T) {
	var raw map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(map[string]any{
			"credential": map[string]any{
				"id":                "cred_clear",
				"owner_user_id":     "user_1",
				"scope":             "personal",
				"weight":            1,
				"max_active_leases": 1,
				"status":            "active",
				"created_at":        time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				"updated_at":        time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := newCredentialTestApp(srv)
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"credentials", "cooldown", "cred_clear", "--clear"})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials cooldown --clear: %v", err)
	}
	if raw["status"] != "active" {
		t.Fatalf("unexpected status payload: %v", raw["status"])
	}
	if raw["cooldown_until"] != "" {
		t.Fatalf("expected cooldown_until clear payload, got %v", raw["cooldown_until"])
	}
}

func newCredentialTestApp(srv *httptest.Server) *app {
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
		output: "json",
	}
}
