package github

import (
	"context"
	"io"
	"net/http"
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

func TestGetPullRequest(t *testing.T) {
	t.Run("returns base and head refs", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodGet, "/repos/owner/repo/pulls/42", func(w http.ResponseWriter, _ *http.Request) {
				pr := PullRequest{Number: 42}
				pr.Base.Ref = "main"
				pr.Head.Ref = "feature/fix-readme"
				writeJSONResponse(t, w, http.StatusOK, pr)
			}),
		)
		pr, err := client.GetPullRequest(context.Background(), "owner/repo", 42)
		if err != nil {
			t.Fatalf("GetPullRequest returned error: %v", err)
		}
		if pr.Number != 42 {
			t.Fatalf("pr number = %d, want 42", pr.Number)
		}
		if pr.Base.Ref != "main" {
			t.Fatalf("base ref = %q, want main", pr.Base.Ref)
		}
		if pr.Head.Ref != "feature/fix-readme" {
			t.Fatalf("head ref = %q, want feature/fix-readme", pr.Head.Ref)
		}
	})

	t.Run("rejects invalid pull number", func(t *testing.T) {
		client := NewAPIClient("token")
		_, err := client.GetPullRequest(context.Background(), "owner/repo", 0)
		if err == nil || !strings.Contains(err.Error(), "pull number must be positive") {
			t.Fatalf("expected pull number error, got: %v", err)
		}
	})
}

func TestFindWebhookByURL(t *testing.T) {
	client := newGitHubMockClient(t,
		githubRoute(http.MethodGet, "/repos/owner/repo/hooks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONResponse(t, w, http.StatusOK, []webhookAPIResponse{
				{
					ID:     1,
					Active: true,
					Events: []string{"issues"},
					Config: webhookConfig{URL: "https://example.com/a"},
				},
				{
					ID:     2,
					Active: true,
					Events: []string{"issues", "issue_comment"},
					Config: webhookConfig{URL: "https://example.com/b"},
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
	} else if hook.ID != 2 {
		t.Fatalf("unexpected hook id: %d", hook.ID)
	}
}

func TestDeleteWebhookByURL(t *testing.T) {
	deleted := false
	client := newGitHubMockClient(t,
		githubRoute(http.MethodGet, "/repos/owner/repo/hooks", func(w http.ResponseWriter, _ *http.Request) {
			writeJSONResponse(t, w, http.StatusOK, []webhookAPIResponse{
				{
					ID:     22,
					Active: true,
					Events: []string{"issues"},
					Config: webhookConfig{URL: "https://example.com/hook"},
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
				in := decodeJSONRequest[reactionRequest](t, r)
				if in.Content != ReactionEyes {
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
				if _, err := io.WriteString(w, `{"message":"forbidden"}`); err != nil {
					t.Fatalf("write response: %v", err)
				}
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
				writeJSONResponse(t, w, http.StatusOK, struct {
					Login string `json:"login"`
				}{Login: "rascalbot"})
			}),
			githubRoute(http.MethodGet, "/repos/owner/repo/issues/42/reactions", func(w http.ResponseWriter, _ *http.Request) {
				writeJSONResponse(t, w, http.StatusOK, []issueReaction{
					{ID: 1, User: struct {
						Login string `json:"login"`
					}{Login: "rascalbot"}},
					{ID: 2, User: struct {
						Login string `json:"login"`
					}{Login: "someone-else"}},
					{ID: 3, User: struct {
						Login string `json:"login"`
					}{Login: "RASCALBOT"}},
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
				in := decodeJSONRequest[reactionRequest](t, r)
				if in.Content != ReactionEyes {
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
				in := decodeJSONRequest[issueCommentRequest](t, r)
				if in.Body != "hello from rascal" {
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

func TestAddPullRequestReviewReaction(t *testing.T) {
	t.Run("posts reaction", func(t *testing.T) {
		client := newGitHubMockClient(t,
			githubRoute(http.MethodPost, "/repos/owner/repo/pulls/42/reviews/999/reactions", func(w http.ResponseWriter, r *http.Request) {
				in := decodeJSONRequest[reactionRequest](t, r)
				if in.Content != ReactionEyes {
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
				in := decodeJSONRequest[reactionRequest](t, r)
				if in.Content != ReactionEyes {
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
			writeJSONResponse(t, w, http.StatusOK, []webhookAPIResponse{})
		}),
		githubRoute(http.MethodPost, "/repos/owner/repo/hooks", func(w http.ResponseWriter, r *http.Request) {
			payload := decodeJSONRequest[webhookPayload](t, r)
			receivedEvents = append(receivedEvents, payload.Events...)
			if payload.Config.URL != "https://example.com/hook" {
				t.Fatalf("config url = %q, want webhook URL", payload.Config.URL)
			}
			if payload.Config.ContentType != "json" {
				t.Fatalf("content type = %q, want json", payload.Config.ContentType)
			}
			if payload.Config.Secret != "secret" {
				t.Fatalf("secret = %q, want secret", payload.Config.Secret)
			}
			if !payload.Active {
				t.Fatal("expected webhook payload to be active")
			}
			w.WriteHeader(http.StatusCreated)
			if _, err := io.WriteString(w, `{"id":1}`); err != nil {
				t.Fatalf("write response: %v", err)
			}
		}),
	)

	if err := client.UpsertWebhook(context.Background(), "owner/repo", "https://example.com/hook", "secret", nil); err != nil {
		t.Fatalf("UpsertWebhook returned error: %v", err)
	}

	want := []string{"issues", "issue_comment", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread", "pull_request"}
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
