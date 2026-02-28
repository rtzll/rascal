package github

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
