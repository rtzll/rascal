package main

import (
	"encoding/json"
	"testing"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
)

func TestResolveWebhookTestInputMissingSecret(t *testing.T) {
	t.Setenv("RASCAL_GITHUB_WEBHOOK_SECRET", "")
	t.Setenv("GITHUB_WEBHOOK_SECRET", "")

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
