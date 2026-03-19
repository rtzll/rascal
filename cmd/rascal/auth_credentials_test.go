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
	root := mustNewRootCmd(t)
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

func TestAuthRotateJSON(t *testing.T) {
	a := &app{
		output:     "json",
		configPath: filepath.Join(t.TempDir(), "config.toml"),
	}
	cmd := a.newAuthCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"rotate"})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("auth rotate: %v", err)
	}

	var out authRotateOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v", err)
	}
	if out.APIToken == "" || !strings.Contains(out.APIToken, "*") {
		t.Fatalf("expected masked api token, got %q", out.APIToken)
	}
	if out.WebhookSecret == "" || !strings.Contains(out.WebhookSecret, "*") {
		t.Fatalf("expected masked webhook secret, got %q", out.WebhookSecret)
	}
	if out.WriteConfig {
		t.Fatal("expected write_config=false by default")
	}
	if out.SyncedRemote {
		t.Fatal("expected synced_remote=false by default")
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
		if err := json.NewEncoder(w).Encode(credentialListResponse{
			Credentials: []credentialRecord{{
				ID:          "cred_one",
				OwnerUserID: "user_1",
				Scope:       "personal",
				Weight:      2,
				Status:      "active",
				CreatedAt:   now,
				UpdatedAt:   now,
			}},
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
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:          "cred_create",
				OwnerUserID: "user_1",
				Scope:       payload.Scope,
				Weight:      payload.Weight,
				Status:      "active",
				CreatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
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
	if payload.Provider != "codex" {
		t.Fatalf("payload provider = %q, want codex", payload.Provider)
	}
	if payload.Weight != 5 {
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

func TestAuthCredentialsCreateDeprecatedRuntimeAliasMapsClaudeToAnthropic(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "oauth_token")
	if err := os.WriteFile(authPath, []byte("token"), 0o600); err != nil {
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
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:        "cred_create",
				Scope:     payload.Scope,
				Weight:    payload.Weight,
				Status:    "active",
				CreatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
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
		"--auth-file", authPath,
		"--runtime", "claude",
	})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials create with deprecated alias: %v", err)
	}
	if payload.Provider != "anthropic" {
		t.Fatalf("payload provider = %q, want anthropic", payload.Provider)
	}
}

func TestAuthCredentialsUpdateSendsChangedFields(t *testing.T) {
	var raw credentialUpdateRequest
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
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:          "cred_update",
				OwnerUserID: "user_2",
				Scope:       "personal",
				Weight:      9,
				Status:      "active",
				CreatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
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
		"--provider", "anthropic",
		"--owner-user-id", "user_2",
		"--weight", "9",
		"--auth-blob", `{"token":"updated"}`,
	})
	if _, err := captureStdout(func() error { return cmd.Execute() }); err != nil {
		t.Fatalf("credentials update: %v", err)
	}
	if raw.Scope == nil || *raw.Scope != "personal" {
		t.Fatalf("unexpected scope payload: %v", raw.Scope)
	}
	if raw.Provider == nil || *raw.Provider != "anthropic" {
		t.Fatalf("unexpected provider payload: %v", raw.Provider)
	}
	if raw.OwnerUserID == nil || *raw.OwnerUserID != "user_2" {
		t.Fatalf("unexpected owner payload: %v", raw.OwnerUserID)
	}
	if raw.Weight == nil || *raw.Weight != 9 {
		t.Fatalf("unexpected weight payload: %v", raw.Weight)
	}
	if raw.AuthBlob == nil || *raw.AuthBlob != `{"token":"updated"}` {
		t.Fatalf("unexpected auth blob payload: %v", raw.AuthBlob)
	}
}

func TestAuthCredentialsDisableFetchesUpdatedCredential(t *testing.T) {
	var deletes, gets int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodDelete && r.URL.Path == "/v1/credentials/cred_disable":
			deletes++
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(credentialDisableResponse{Disabled: true}); err != nil {
				t.Fatalf("encode delete response: %v", err)
			}
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/cred_disable":
			gets++
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(credentialGetResponse{
				Credential: credentialRecord{
					ID:          "cred_disable",
					OwnerUserID: "user_1",
					Scope:       "personal",
					Weight:      1,
					Status:      "disabled",
					LastError:   "disabled by API",
					CreatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
					UpdatedAt:   time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
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
	if !out.Disabled || out.Credential == nil || out.Credential.Status != "disabled" {
		t.Fatalf("unexpected disable output: %+v", out)
	}
}

func TestSeedBootstrapSharedCredentialCreatesWhenMissing(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	var seenMethods []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seenMethods = append(seenMethods, r.Method+" "+r.URL.Path)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/"+bootstrapSharedCredentialID:
			http.Error(w, "credential not found", http.StatusNotFound)
		case r.Method == http.MethodPost && r.URL.Path == "/v1/credentials":
			var req credentialCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode create request: %v", err)
			}
			if req.ID != bootstrapSharedCredentialID || req.Scope != "shared" || req.AuthBlob != `{"token":"abc"}` {
				t.Fatalf("unexpected create request: %+v", req)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(credentialGetResponse{
				Credential: credentialRecord{
					ID:        bootstrapSharedCredentialID,
					Scope:     "shared",
					Weight:    1,
					Status:    "active",
					CreatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
					UpdatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				},
			}); err != nil {
				t.Fatalf("encode create response: %v", err)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client := apiClient{
		baseURL: srv.URL,
		token:   "token",
		http:    &http.Client{Timeout: 5 * time.Second},
	}
	cred, err := seedBootstrapSharedCredential(client, authPath)
	if err != nil {
		t.Fatalf("seedBootstrapSharedCredential: %v", err)
	}
	if cred.ID != bootstrapSharedCredentialID {
		t.Fatalf("credential id = %q, want %q", cred.ID, bootstrapSharedCredentialID)
	}
	if strings.Join(seenMethods, ",") != "GET /v1/credentials/"+bootstrapSharedCredentialID+",POST /v1/credentials" {
		t.Fatalf("unexpected request flow: %v", seenMethods)
	}
}

func TestSeedBootstrapSharedCredentialUpdatesWhenPresent(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"updated"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	var raw credentialUpdateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/v1/credentials/"+bootstrapSharedCredentialID:
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(credentialGetResponse{
				Credential: credentialRecord{
					ID:        bootstrapSharedCredentialID,
					Scope:     "shared",
					Weight:    2,
					Status:    "cooldown",
					CreatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
					UpdatedAt: time.Date(2026, 3, 8, 12, 1, 0, 0, time.UTC),
				},
			}); err != nil {
				t.Fatalf("encode get response: %v", err)
			}
		case r.Method == http.MethodPatch && r.URL.Path == "/v1/credentials/"+bootstrapSharedCredentialID:
			if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
				t.Fatalf("decode patch request: %v", err)
			}
			w.Header().Set("Content-Type", "application/json")
			if err := json.NewEncoder(w).Encode(credentialGetResponse{
				Credential: credentialRecord{
					ID:        bootstrapSharedCredentialID,
					Scope:     "shared",
					Weight:    2,
					Status:    "active",
					CreatedAt: time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
					UpdatedAt: time.Date(2026, 3, 8, 12, 2, 0, 0, time.UTC),
				},
			}); err != nil {
				t.Fatalf("encode patch response: %v", err)
			}
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	t.Cleanup(srv.Close)

	client := apiClient{
		baseURL: srv.URL,
		token:   "token",
		http:    &http.Client{Timeout: 5 * time.Second},
	}
	if _, err := seedBootstrapSharedCredential(client, authPath); err != nil {
		t.Fatalf("seedBootstrapSharedCredential: %v", err)
	}
	if raw.Scope == nil || *raw.Scope != "shared" || raw.Status == nil || *raw.Status != "active" || raw.AuthBlob == nil || *raw.AuthBlob != `{"token":"updated"}` {
		t.Fatalf("unexpected patch payload: %+v", raw)
	}
	if raw.CooldownUntil == nil || *raw.CooldownUntil != "" || raw.LastError == nil || *raw.LastError != "" {
		t.Fatalf("expected cooldown state cleared, got %+v", raw)
	}
}

func TestAuthCredentialsEnableClearsCooldown(t *testing.T) {
	var raw credentialUpdateRequest
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
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:          "cred_enable",
				OwnerUserID: "user_1",
				Scope:       "personal",
				Weight:      1,
				Status:      "active",
				CreatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
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
	if raw.Status == nil || *raw.Status != "active" {
		t.Fatalf("unexpected status payload: %v", raw.Status)
	}
	if raw.CooldownUntil == nil || *raw.CooldownUntil != "" {
		t.Fatalf("expected cooldown_until clear payload, got %v", raw.CooldownUntil)
	}
	if raw.LastError == nil || *raw.LastError != "" {
		t.Fatalf("expected last_error clear payload, got %v", raw.LastError)
	}
}

func TestAuthCredentialsCooldownSetsCooldown(t *testing.T) {
	var raw credentialUpdateRequest
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
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:            "cred_cooldown",
				OwnerUserID:   "user_1",
				Scope:         "personal",
				Weight:        1,
				Status:        "cooldown",
				CooldownUntil: timePtr(time.Date(2026, 3, 8, 13, 0, 0, 0, time.UTC)),
				LastError:     "manual cooldown",
				CreatedAt:     time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt:     time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
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
	if raw.Status == nil || *raw.Status != "cooldown" {
		t.Fatalf("unexpected status payload: %v", raw.Status)
	}
	if raw.LastError == nil || *raw.LastError != "manual cooldown" {
		t.Fatalf("unexpected last_error payload: %v", raw.LastError)
	}
	if raw.CooldownUntil == nil || strings.TrimSpace(*raw.CooldownUntil) == "" {
		t.Fatalf("expected cooldown_until payload, got %v", raw.CooldownUntil)
	}
	if _, err := time.Parse(time.RFC3339, *raw.CooldownUntil); err != nil {
		t.Fatalf("cooldown_until should be RFC3339, got %q (%v)", *raw.CooldownUntil, err)
	}
}

func TestAuthCredentialsCooldownClear(t *testing.T) {
	var raw credentialUpdateRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPatch {
			t.Fatalf("unexpected method: %s", r.Method)
		}
		if err := json.NewDecoder(r.Body).Decode(&raw); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(credentialGetResponse{
			Credential: credentialRecord{
				ID:          "cred_clear",
				OwnerUserID: "user_1",
				Scope:       "personal",
				Weight:      1,
				Status:      "active",
				CreatedAt:   time.Date(2026, 3, 8, 12, 0, 0, 0, time.UTC),
				UpdatedAt:   time.Date(2026, 3, 8, 12, 5, 0, 0, time.UTC),
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
	if raw.Status == nil || *raw.Status != "active" {
		t.Fatalf("unexpected status payload: %v", raw.Status)
	}
	if raw.CooldownUntil == nil || *raw.CooldownUntil != "" {
		t.Fatalf("expected cooldown_until clear payload, got %v", raw.CooldownUntil)
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

func timePtr(t time.Time) *time.Time {
	return &t
}
