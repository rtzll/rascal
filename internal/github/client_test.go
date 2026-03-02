package github

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func newTestAPIClient(serverURL string) *APIClient {
	c := NewAPIClient("token")
	c.baseURL = serverURL
	c.http = &http.Client{Timeout: 2 * time.Second}
	return c
}

func TestLabelExists(t *testing.T) {
	t.Run("label exists", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/repos/owner/repo/labels/rascal" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			w.WriteHeader(http.StatusOK)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		ok, err := client.LabelExists(context.Background(), "owner/repo", "rascal")
		if err != nil {
			t.Fatalf("LabelExists returned error: %v", err)
		}
		if !ok {
			t.Fatal("expected label to exist")
		}
	})

	t.Run("label missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		ok, err := client.LabelExists(context.Background(), "owner/repo", "rascal")
		if err != nil {
			t.Fatalf("LabelExists returned error: %v", err)
		}
		if ok {
			t.Fatal("expected label to be missing")
		}
	})
}

func TestFindWebhookByURL(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/owner/repo/hooks" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"id":     1,
				"active": true,
				"events": []string{"issues"},
				"config": map[string]any{"url": "https://example.com/a"},
			},
			{
				"id":     2,
				"active": true,
				"events": []string{"issues", "issue_comment"},
				"config": map[string]any{"url": "https://example.com/b"},
			},
		})
	}))
	defer srv.Close()

	client := newTestAPIClient(srv.URL)
	hook, err := client.FindWebhookByURL(context.Background(), "owner/repo", "https://example.com/b")
	if err != nil {
		t.Fatalf("FindWebhookByURL returned error: %v", err)
	}
	if hook == nil {
		t.Fatal("expected webhook to be found")
	}
	if hook.ID != 2 {
		t.Fatalf("unexpected hook id: %d", hook.ID)
	}
}

func TestDeleteWebhookByURL(t *testing.T) {
	deleted := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/hooks":
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"id":     22,
					"active": true,
					"events": []string{"issues"},
					"config": map[string]any{"url": "https://example.com/hook"},
				},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/repos/owner/repo/hooks/22":
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestAPIClient(srv.URL)
	removed, err := client.DeleteWebhookByURL(context.Background(), "owner/repo", "https://example.com/hook")
	if err != nil {
		t.Fatalf("DeleteWebhookByURL returned error: %v", err)
	}
	if !removed {
		t.Fatal("expected removed=true")
	}
	if !deleted {
		t.Fatal("expected delete endpoint to be called")
	}
}

func TestDescribeWebhookAuthFailure(t *testing.T) {
	msg := describeWebhookAuthFailure(http.StatusForbidden, []byte(`{"message":"Resource not accessible by personal access token"}`))
	if !strings.Contains(msg, "Webhooks: Read and write") {
		t.Fatalf("expected webhook permission hint, got: %s", msg)
	}

	plain := describeWebhookAuthFailure(http.StatusBadRequest, []byte(`oops`))
	if plain != "oops" {
		t.Fatalf("unexpected message passthrough: %s", plain)
	}
}

func TestAddIssueReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.URL.Path != "/repos/owner/repo/issues/42/reactions" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if in["content"] != ReactionEyes {
				t.Fatalf("unexpected reaction payload: %v", in)
			}
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		if err := client.AddIssueReaction(context.Background(), "owner/repo", 42, ReactionEyes); err != nil {
			t.Fatalf("AddIssueReaction returned error: %v", err)
		}
	})

	t.Run("rejects unsupported reaction", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.AddIssueReaction(context.Background(), "owner/repo", 42, "check")
		if err == nil || !strings.Contains(err.Error(), "unsupported reaction content") {
			t.Fatalf("expected unsupported content error, got: %v", err)
		}
	})

	t.Run("surfaces github error", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusForbidden)
			_, _ = io.WriteString(w, `{"message":"forbidden"}`)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		err := client.AddIssueReaction(context.Background(), "owner/repo", 42, ReactionRocket)
		if err == nil || !strings.Contains(err.Error(), "github add issue reaction failed") {
			t.Fatalf("expected github error, got: %v", err)
		}
	})
}

func TestAddIssueCommentReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.URL.Path != "/repos/owner/repo/issues/comments/123/reactions" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if in["content"] != ReactionEyes {
				t.Fatalf("unexpected reaction payload: %v", in)
			}
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		if err := client.AddIssueCommentReaction(context.Background(), "owner/repo", 123, ReactionEyes); err != nil {
			t.Fatalf("AddIssueCommentReaction returned error: %v", err)
		}
	})

	t.Run("rejects invalid id", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.AddIssueCommentReaction(context.Background(), "owner/repo", 0, ReactionEyes)
		if err == nil || !strings.Contains(err.Error(), "comment id must be positive") {
			t.Fatalf("expected comment id error, got: %v", err)
		}
	})
}

func TestAddPullRequestReviewReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodPost {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.URL.Path != "/repos/owner/repo/pulls/42/reviews/999/reactions" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			var in map[string]string
			if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			if in["content"] != ReactionEyes {
				t.Fatalf("unexpected reaction payload: %v", in)
			}
			w.WriteHeader(http.StatusCreated)
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		if err := client.AddPullRequestReviewReaction(context.Background(), "owner/repo", 42, 999, ReactionEyes); err != nil {
			t.Fatalf("AddPullRequestReviewReaction returned error: %v", err)
		}
	})

	t.Run("rejects invalid ids", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.AddPullRequestReviewReaction(context.Background(), "owner/repo", 0, 999, ReactionEyes)
		if err == nil || !strings.Contains(err.Error(), "pull number must be positive") {
			t.Fatalf("expected pull number error, got: %v", err)
		}
		err = client.AddPullRequestReviewReaction(context.Background(), "owner/repo", 42, 0, ReactionEyes)
		if err == nil || !strings.Contains(err.Error(), "review id must be positive") {
			t.Fatalf("expected review id error, got: %v", err)
		}
	})
}

func TestUpsertWebhookDefaultEventsIncludeReviewComment(t *testing.T) {
	var receivedEvents []string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/repos/owner/repo/hooks":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case r.Method == http.MethodPost && r.URL.Path == "/repos/owner/repo/hooks":
			var payload struct {
				Events []string `json:"events"`
			}
			if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
				t.Fatalf("decode request body: %v", err)
			}
			receivedEvents = append(receivedEvents, payload.Events...)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
		default:
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer srv.Close()

	client := newTestAPIClient(srv.URL)
	if err := client.UpsertWebhook(context.Background(), "owner/repo", "https://example.com/hook", "secret", nil); err != nil {
		t.Fatalf("UpsertWebhook returned error: %v", err)
	}

	want := []string{"issues", "issue_comment", "pull_request_review", "pull_request_review_comment", "pull_request"}
	for _, ev := range want {
		found := false
		for _, got := range receivedEvents {
			if got == ev {
				found = true
				break
			}
		}
		if !found {
			t.Fatalf("default webhook events missing %q: %v", ev, receivedEvents)
		}
	}
}
