package main

import (
	"reflect"
	"testing"
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
