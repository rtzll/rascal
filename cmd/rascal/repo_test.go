package main

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
)

func TestMissingRequiredWebhookEvents(t *testing.T) {
	t.Run("all required events present", func(t *testing.T) {
		events := []string{
			"pull_request_review",
			"pull_request_review_comment",
			"issue_comment",
			"issues",
			"pull_request",
		}
		if missing := missingRequiredWebhookEvents(events); len(missing) != 0 {
			t.Fatalf("expected no missing events, got %v", missing)
		}
	})

	t.Run("normalizes case and trims", func(t *testing.T) {
		events := []string{
			" Issues ",
			"ISSUE_COMMENT",
			"pull_request_review",
			"pull_request_review_comment",
			"pull_request",
			"pull_request",
		}
		if missing := missingRequiredWebhookEvents(events); len(missing) != 0 {
			t.Fatalf("expected no missing events, got %v", missing)
		}
	})

	t.Run("returns missing events in required order", func(t *testing.T) {
		events := []string{"issues", "issue_comment", "pull_request"}
		want := []string{"pull_request_review", "pull_request_review_comment"}
		got := missingRequiredWebhookEvents(events)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("missing = %v, want %v", got, want)
		}
	})
}

type fakeRepoClient struct {
	ensureCalled  bool
	ensureRepo    string
	ensureName    string
	ensureColor   string
	ensureDesc    string
	webhookCalled bool
	webhookRepo   string
	webhookURL    string
	webhookSecret string
	webhookEvents []string
}

func (f *fakeRepoClient) EnsureLabel(_ context.Context, repo, name, color, description string) error {
	f.ensureCalled = true
	f.ensureRepo = repo
	f.ensureName = name
	f.ensureColor = color
	f.ensureDesc = description
	return nil
}

func (f *fakeRepoClient) UpsertWebhook(_ context.Context, repo, webhookURL, secret string, events []string) error {
	f.webhookCalled = true
	f.webhookRepo = repo
	f.webhookURL = webhookURL
	f.webhookSecret = secret
	f.webhookEvents = events
	return nil
}

func TestRunRepoEnableUsesProvidedClient(t *testing.T) {
	client := &fakeRepoClient{}
	a := &app{
		cfg: config.ClientConfig{
			ServerURL: "http://example.com",
		},
	}
	result, err := a.runRepoEnable(repoEnableInput{
		Repo:          "owner/repo",
		GitHubToken:   "token",
		WebhookSecret: "secret",
		Client:        client,
		Timeout:       5 * time.Second,
	})
	if err != nil {
		t.Fatalf("runRepoEnable failed: %v", err)
	}
	if !client.ensureCalled || !client.webhookCalled {
		t.Fatalf("expected ensure label and webhook calls, got ensure=%t webhook=%t", client.ensureCalled, client.webhookCalled)
	}
	if client.ensureRepo != "owner/repo" || client.ensureName != "rascal" || client.ensureColor != "0e8a16" {
		t.Fatalf("unexpected ensure label args: repo=%s name=%s color=%s", client.ensureRepo, client.ensureName, client.ensureColor)
	}
	if client.ensureDesc != "Trigger Rascal automation" {
		t.Fatalf("unexpected ensure label description: %s", client.ensureDesc)
	}
	if client.webhookURL != "http://example.com/v1/webhooks/github" {
		t.Fatalf("unexpected webhook url: %s", client.webhookURL)
	}
	if client.webhookSecret != "secret" {
		t.Fatalf("unexpected webhook secret: %s", client.webhookSecret)
	}
	if result.WebhookURL != "http://example.com/v1/webhooks/github" {
		t.Fatalf("unexpected result webhook url: %s", result.WebhookURL)
	}
}
