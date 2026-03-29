package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	"github.com/pelletier/go-toml/v2"
	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/spf13/cobra"
)

func mustNewRootCmd(t *testing.T) *cobra.Command {
	t.Helper()
	return newRootCmd()
}

func mustNewRunCmd(t *testing.T, a *app) *cobra.Command {
	t.Helper()
	return a.newRunCmd()
}

func mustNewDeployCmd(t *testing.T, a *app) *cobra.Command {
	t.Helper()
	return a.newDeployCmd()
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
	root := mustNewRootCmd(t)
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
