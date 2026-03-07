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
		client := newGitHubMockClient(t,
			githubRoute(http.MethodGet, "/repos/owner/repo/labels/rascal", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}),
		)
		ok, err := client.LabelExists(context.Background(), "owner/repo", "rascal")
		if err != nil {
			t.Fatalf("LabelExists returned error: %v", err)
		}
		if !ok {
			t.Fatal("expected label to exist")
		}
	})

	t.Run("label missing", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodGet, "/repos/owner/repo/labels/rascal", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			}),
		)
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
	client := newGitHubMockClient(t,
		githubRoute(http.MethodGet, "/repos/owner/repo/hooks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONResponse(t, w, http.StatusOK, []map[string]any{
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
		}),
	)
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
	client := newGitHubMockClient(t,
		githubRoute(http.MethodGet, "/repos/owner/repo/hooks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONResponse(t, w, http.StatusOK, []map[string]any{
				{
					"id":     22,
					"active": true,
					"events": []string{"issues"},
					"config": map[string]any{"url": "https://example.com/hook"},
				},
			})
		}),
		githubRoute(http.MethodDelete, "/repos/owner/repo/hooks/22", func(w http.ResponseWriter, _ *http.Request) {
			deleted = true
			w.WriteHeader(http.StatusNoContent)
		}),
	)
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
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/issues/42/reactions", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[map[string]string](t, r)
				if in["content"] != ReactionEyes {
					t.Fatalf("unexpected reaction payload: %v", in)
				}
				w.WriteHeader(http.StatusCreated)
			}),
		)
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
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/issues/42/reactions", func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusForbidden)
				_, _ = io.WriteString(w, `{"message":"forbidden"}`)
			}),
		)
		err := client.AddIssueReaction(context.Background(), "owner/repo", 42, ReactionRocket)
		if err == nil || !strings.Contains(err.Error(), "github add issue reaction failed") {
			t.Fatalf("expected github error, got: %v", err)
		}
	})
}

func TestRemoveIssueReactions(t *testing.T) {
	t.Run("removes only authenticated user reactions", func(t *testing.T) {
		deleted := []string{}
		client := newGitHubMockClient(t,
			githubRoute(http.MethodGet, "/user", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, w, http.StatusOK, map[string]any{"login": "rascalbot"})
			}),
			githubRoute(http.MethodGet, "/repos/owner/repo/issues/42/reactions", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, w, http.StatusOK, []map[string]any{
					{"id": 1, "user": map[string]any{"login": "rascalbot"}},
					{"id": 2, "user": map[string]any{"login": "someone-else"}},
					{"id": 3, "user": map[string]any{"login": "RASCALBOT"}},
				})
			}),
			githubRoute(http.MethodDelete, "/repos/owner/repo/issues/42/reactions/1", func(w http.ResponseWriter, _ *http.Request) {
				deleted = append(deleted, "1")
				w.WriteHeader(http.StatusNoContent)
			}),
			githubRoute(http.MethodDelete, "/repos/owner/repo/issues/42/reactions/3", func(w http.ResponseWriter, _ *http.Request) {
				deleted = append(deleted, "3")
				w.WriteHeader(http.StatusNoContent)
			}),
		)
		if err := client.RemoveIssueReactions(context.Background(), "owner/repo", 42); err != nil {
			t.Fatalf("RemoveIssueReactions returned error: %v", err)
		}
		if strings.Join(deleted, ",") != "1,3" {
			t.Fatalf("unexpected deleted reactions: %v", deleted)
		}
	})

	t.Run("rejects invalid issue number", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.RemoveIssueReactions(context.Background(), "owner/repo", 0)
		if err == nil || !strings.Contains(err.Error(), "issue number must be positive") {
			t.Fatalf("expected issue number error, got: %v", err)
		}
	})
}

func TestAddIssueCommentReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/issues/comments/123/reactions", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[map[string]string](t, r)
				if in["content"] != ReactionEyes {
					t.Fatalf("unexpected reaction payload: %v", in)
				}
				w.WriteHeader(http.StatusCreated)
			}),
		)
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

func TestCreateIssueComment(t *testing.T) {
	t.Run("posts issue comment", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/issues/42/comments", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[map[string]string](t, r)
				if in["body"] != "hello from rascal" {
					t.Fatalf("unexpected comment payload: %v", in)
				}
				w.WriteHeader(http.StatusCreated)
			}),
		)
		if err := client.CreateIssueComment(context.Background(), "owner/repo", 42, "hello from rascal"); err != nil {
			t.Fatalf("CreateIssueComment returned error: %v", err)
		}
	})

	t.Run("rejects invalid issue number", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.CreateIssueComment(context.Background(), "owner/repo", 0, "x")
		if err == nil || !strings.Contains(err.Error(), "issue number must be positive") {
			t.Fatalf("expected issue number error, got: %v", err)
		}
	})

	t.Run("rejects empty body", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.CreateIssueComment(context.Background(), "owner/repo", 1, "   ")
		if err == nil || !strings.Contains(err.Error(), "comment body is required") {
			t.Fatalf("expected comment body error, got: %v", err)
		}
	})
}

func TestIssueCommentHasToken(t *testing.T) {
	t.Run("finds token in comments", func(t *testing.T) {
		token := "<!-- rascal:run_id=run_123 -->"
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Fatalf("unexpected method: %s", r.Method)
			}
			if r.URL.Path != "/repos/owner/repo/issues/42/comments" {
				t.Fatalf("unexpected path: %s", r.URL.Path)
			}
			if got := r.URL.Query().Get("per_page"); got != "100" {
				t.Fatalf("unexpected per_page: %s", got)
			}
			if got := r.URL.Query().Get("page"); got != "1" {
				t.Fatalf("unexpected page: %s", got)
			}
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"body": "first comment"},
				{"body": "second " + token},
			})
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		found, err := client.IssueCommentHasToken(context.Background(), "owner/repo", 42, token)
		if err != nil {
			t.Fatalf("IssueCommentHasToken returned error: %v", err)
		}
		if !found {
			t.Fatalf("expected token to be found in comments")
		}
	})

	t.Run("returns false when missing", func(t *testing.T) {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_ = json.NewEncoder(w).Encode([]map[string]string{
				{"body": "first comment"},
			})
		}))
		defer srv.Close()

		client := newTestAPIClient(srv.URL)
		found, err := client.IssueCommentHasToken(context.Background(), "owner/repo", 42, "missing")
		if err != nil {
			t.Fatalf("IssueCommentHasToken returned error: %v", err)
		}
		if found {
			t.Fatalf("expected token to be absent")
		}
	})
}

func TestAddPullRequestReviewReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/pulls/42/reviews/999/reactions", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[map[string]string](t, r)
				if in["content"] != ReactionEyes {
					t.Fatalf("unexpected reaction payload: %v", in)
				}
				w.WriteHeader(http.StatusCreated)
			}),
		)
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

func TestAddPullRequestReviewCommentReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/pulls/comments/777/reactions", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[map[string]string](t, r)
				if in["content"] != ReactionEyes {
					t.Fatalf("unexpected reaction payload: %v", in)
				}
				w.WriteHeader(http.StatusCreated)
			}),
		)
		if err := client.AddPullRequestReviewCommentReaction(context.Background(), "owner/repo", 777, ReactionEyes); err != nil {
			t.Fatalf("AddPullRequestReviewCommentReaction returned error: %v", err)
		}
	})

	t.Run("rejects invalid id", func(t *testing.T) {
		client := NewAPIClient("token")
		err := client.AddPullRequestReviewCommentReaction(context.Background(), "owner/repo", 0, ReactionEyes)
		if err == nil || !strings.Contains(err.Error(), "comment id must be positive") {
			t.Fatalf("expected comment id error, got: %v", err)
		}
	})
}

func TestUpsertWebhookDefaultEventsIncludeReviewComment(t *testing.T) {
	var receivedEvents []string

	client := newGitHubMockClient(t,
		githubRoute(http.MethodGet, "/repos/owner/repo/hooks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONResponse(t, w, http.StatusOK, []map[string]any{})
		}),
		githubRoute(http.MethodPost, "/repos/owner/repo/hooks", func(w http.ResponseWriter, r *http.Request) {
			payload := decodeJSONRequest[struct {
				Events []string `json:"events"`
			}](t, r)
			receivedEvents = append(receivedEvents, payload.Events...)
			w.WriteHeader(http.StatusCreated)
			_, _ = io.WriteString(w, `{"id":1}`)
		}),
	)

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
