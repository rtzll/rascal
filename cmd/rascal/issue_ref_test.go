package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/config"
	"github.com/rtzll/rascal/internal/state"
)

func TestParseIssueRef(t *testing.T) {
	t.Parallel()

	ref, err := parseIssueRef(" Owner/Repo#123 ")
	if err != nil {
		t.Fatalf("parseIssueRef: %v", err)
	}
	if ref.Repo != "owner/repo" {
		t.Fatalf("repo = %q, want owner/repo", ref.Repo)
	}
	if ref.IssueNumber != 123 {
		t.Fatalf("issue number = %d, want 123", ref.IssueNumber)
	}
}

func TestParseIssueRefRejectsInvalidInput(t *testing.T) {
	t.Parallel()

	cases := []string{
		"",
		"owner/repo",
		"owner/repo#0",
		"#12",
		"owner/repo#abc",
		"owner/repo#12#13",
	}
	for _, input := range cases {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			if _, err := parseIssueRef(input); err == nil {
				t.Fatalf("expected parseIssueRef(%q) to fail", input)
			}
		})
	}
}

func TestNormalizeIssueLikeTaskID(t *testing.T) {
	t.Parallel()

	if got := normalizeIssueLikeTaskID(" Owner/Repo#42 "); got != "owner/repo#42" {
		t.Fatalf("normalizeIssueLikeTaskID(issue ref) = %q, want owner/repo#42", got)
	}
	if got := normalizeIssueLikeTaskID("custom-task-id"); got != "custom-task-id" {
		t.Fatalf("normalizeIssueLikeTaskID(custom) = %q, want custom-task-id", got)
	}
}

func TestTaskCommandNormalizesIssueLikeTaskID(t *testing.T) {
	t.Parallel()

	var requestedPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requestedPath = r.URL.EscapedPath()
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(api.TaskResponse{Task: state.Task{
			ID:        "owner/repo#42",
			Repo:      "owner/repo",
			Status:    state.TaskOpen,
			UpdatedAt: time.Unix(0, 0).UTC(),
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

	cmd := a.newTaskCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{"Owner/Repo#42"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("task command: %v", err)
	}

	if !strings.HasSuffix(requestedPath, "/v1/tasks/owner%2Frepo%2342") {
		t.Fatalf("requested path = %q, want normalized issue-like task id", requestedPath)
	}
}
