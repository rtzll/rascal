package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
)

type webhookTestJSONRequest struct {
	Method  string            `json:"method"`
	URL     string            `json:"url"`
	Headers map[string]string `json:"headers"`
	Payload json.RawMessage   `json:"payload"`
}

type webhookTestJSONResponse struct {
	Status  string            `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type webhookTestJSONOutput struct {
	WebhookURL      string                   `json:"webhook_url"`
	Repo            string                   `json:"repo"`
	Event           string                   `json:"event"`
	DryRun          bool                     `json:"dry_run"`
	Signature       string                   `json:"signature"`
	DeliveryID      string                   `json:"delivery_id"`
	Payload         json.RawMessage          `json:"payload"`
	Status          int                      `json:"status"`
	StatusText      string                   `json:"status_text"`
	RequestID       string                   `json:"request_id"`
	ResponseSnippet string                   `json:"response_snippet"`
	Request         *webhookTestJSONRequest  `json:"request"`
	Response        *webhookTestJSONResponse `json:"response"`
}

func TestResolveWebhookTestInputMissingSecret(t *testing.T) {
	t.Setenv("RASCAL_GITHUB_WEBHOOK_SECRET", "")

	_, err := resolveWebhookTestInput(webhookTestInput{
		ServerURL: "http://localhost:8080",
		Repo:      "owner/repo",
		Event:     "issues",
	}, config.ClientConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
	ce, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if ce.Message != "missing webhook secret" {
		t.Fatalf("unexpected message: %q", ce.Message)
	}
}

func TestResolveWebhookTestInputUsesCanonicalWebhookSecretEnv(t *testing.T) {
	t.Setenv("RASCAL_GITHUB_WEBHOOK_SECRET", "env-secret")

	got, err := resolveWebhookTestInput(webhookTestInput{
		ServerURL: "http://localhost:8080",
		Repo:      "owner/repo",
		Event:     "issues",
	}, config.ClientConfig{})
	if err != nil {
		t.Fatalf("resolveWebhookTestInput failed: %v", err)
	}
	if got.WebhookSecret != "env-secret" {
		t.Fatalf("webhook secret = %q, want env-secret", got.WebhookSecret)
	}
}

func TestResolveWebhookTestInputMissingRepo(t *testing.T) {
	_, err := resolveWebhookTestInput(webhookTestInput{
		ServerURL:     "http://localhost:8080",
		WebhookSecret: "secret",
		Event:         "issues",
	}, config.ClientConfig{})
	if err == nil {
		t.Fatal("expected error")
	}
	ce, ok := err.(*cliError)
	if !ok {
		t.Fatalf("expected cliError, got %T", err)
	}
	if ce.Message != "repo is required" {
		t.Fatalf("unexpected message: %q", ce.Message)
	}
}

func TestBuildWebhookTestPayloadTemplates(t *testing.T) {
	repo := "acme/widgets"
	cases := []struct {
		event string
		check func(*testing.T, []byte)
	}{
		{
			event: "issues",
			check: func(t *testing.T, payload []byte) {
				var ev ghapi.IssuesEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					t.Fatalf("unmarshal issues: %v", err)
				}
				if ev.Action != "labeled" {
					t.Fatalf("unexpected action: %q", ev.Action)
				}
				if ev.Label.Name != "rascal" {
					t.Fatalf("unexpected label: %q", ev.Label.Name)
				}
				if ev.Issue.PullRequest != nil {
					t.Fatal("expected issue to not be a pull request")
				}
				if ev.Repository.FullName != repo {
					t.Fatalf("unexpected repo: %q", ev.Repository.FullName)
				}
			},
		},
		{
			event: "issue_comment",
			check: func(t *testing.T, payload []byte) {
				var ev ghapi.IssueCommentEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					t.Fatalf("unmarshal issue_comment: %v", err)
				}
				if ev.Action != "created" {
					t.Fatalf("unexpected action: %q", ev.Action)
				}
				if ev.Issue.PullRequest == nil {
					t.Fatal("expected pull_request marker")
				}
				if ev.Issue.PullRequest.URL != "https://example.com/pull/123" {
					t.Fatalf("unexpected pull_request url: %q", ev.Issue.PullRequest.URL)
				}
				if ev.Comment.ID == 0 {
					t.Fatal("expected comment id")
				}
				if ev.Repository.FullName != repo {
					t.Fatalf("unexpected repo: %q", ev.Repository.FullName)
				}
			},
		},
		{
			event: "pull_request_review",
			check: func(t *testing.T, payload []byte) {
				var ev ghapi.PullRequestReviewEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					t.Fatalf("unmarshal pull_request_review: %v", err)
				}
				if ev.Action != "submitted" {
					t.Fatalf("unexpected action: %q", ev.Action)
				}
				if ev.Review.ID == 0 {
					t.Fatal("expected review id")
				}
				if ev.PullRequest.Number == 0 {
					t.Fatal("expected pull request number")
				}
				if ev.Repository.FullName != repo {
					t.Fatalf("unexpected repo: %q", ev.Repository.FullName)
				}
			},
		},
		{
			event: "pull_request_review_comment",
			check: func(t *testing.T, payload []byte) {
				var ev ghapi.PullRequestReviewCommentEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					t.Fatalf("unmarshal pull_request_review_comment: %v", err)
				}
				if ev.Action != "created" {
					t.Fatalf("unexpected action: %q", ev.Action)
				}
				if ev.Comment.ID == 0 {
					t.Fatal("expected review comment id")
				}
				if ev.Comment.Path == "" {
					t.Fatal("expected review comment path")
				}
				if ev.Comment.Line == nil || *ev.Comment.Line <= 0 {
					t.Fatal("expected review comment line")
				}
				if ev.PullRequest.Number == 0 {
					t.Fatal("expected pull request number")
				}
				if ev.Repository.FullName != repo {
					t.Fatalf("unexpected repo: %q", ev.Repository.FullName)
				}
			},
		},
		{
			event: "pull_request",
			check: func(t *testing.T, payload []byte) {
				var ev ghapi.PullRequestEvent
				if err := json.Unmarshal(payload, &ev); err != nil {
					t.Fatalf("unmarshal pull_request: %v", err)
				}
				if ev.Action != "opened" {
					t.Fatalf("unexpected action: %q", ev.Action)
				}
				if ev.PullRequest.Number == 0 {
					t.Fatal("expected pull request number")
				}
				if ev.Repository.FullName != repo {
					t.Fatalf("unexpected repo: %q", ev.Repository.FullName)
				}
			},
		},
	}

	for _, tc := range cases {
		payload, err := buildWebhookTestPayload(tc.event, repo)
		if err != nil {
			t.Fatalf("build payload %s: %v", tc.event, err)
		}
		tc.check(t, payload)
	}
}

func TestBuildWebhookTestPayloadUnknownEvent(t *testing.T) {
	if _, err := buildWebhookTestPayload("unknown", "owner/repo"); err == nil {
		t.Fatal("expected error for unknown event")
	}
}

func TestWebhookTestDryRunJSONOutput(t *testing.T) {
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://localhost:8080",
		},
		output: "json",
		quiet:  true,
	}
	cmd := a.newWebhookTestCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--webhook-secret", "secret",
		"--dry-run",
		"--verbose",
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("webhook test --dry-run: %v", err)
	}

	var out webhookTestJSONOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v\noutput:\n%s", err, stdout)
	}
	if !out.DryRun {
		t.Fatal("expected dry_run=true")
	}
	if out.Request == nil {
		t.Fatal("expected request payload in verbose dry-run output")
	}
	if out.Request.Method != http.MethodPost {
		t.Fatalf("request method = %q, want POST", out.Request.Method)
	}
	if len(out.Payload) == 0 || !json.Valid(out.Payload) {
		t.Fatalf("expected raw JSON payload, got %s", out.Payload)
	}
}

func TestWebhookTestLiveJSONOutput(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-ID", "req_123")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusAccepted)
		if _, err := w.Write([]byte(`{"ok":true}`)); err != nil {
			t.Fatalf("write response: %v", err)
		}
	}))
	t.Cleanup(srv.Close)

	a := &app{
		cfg: config.ClientConfig{
			ServerURL: srv.URL,
		},
		output: "json",
		quiet:  true,
	}
	cmd := a.newWebhookTestCmd()
	cmd.SetOut(io.Discard)
	cmd.SetErr(io.Discard)
	cmd.SetArgs([]string{
		"--repo", "owner/repo",
		"--webhook-secret", "secret",
		"--verbose",
	})
	stdout, err := captureStdout(func() error { return cmd.Execute() })
	if err != nil {
		t.Fatalf("webhook test: %v", err)
	}

	var out webhookTestJSONOutput
	if err := json.Unmarshal([]byte(stdout), &out); err != nil {
		t.Fatalf("decode output: %v\noutput:\n%s", err, stdout)
	}
	if out.Status != http.StatusAccepted {
		t.Fatalf("status = %d, want %d", out.Status, http.StatusAccepted)
	}
	if out.RequestID != "req_123" {
		t.Fatalf("request_id = %q, want req_123", out.RequestID)
	}
	if out.Response == nil || out.Response.Status != "202 Accepted" {
		t.Fatalf("unexpected response: %+v", out.Response)
	}
	if out.Request == nil {
		t.Fatal("expected request payload in verbose live output")
	}
}
