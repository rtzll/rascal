package github

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
)

type WebhookData struct {
	ID     int      `json:"id"`
	URL    string   `json:"url"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
}

type webhookConfig struct {
	URL         string `json:"url"`
	ContentType string `json:"content_type,omitempty"`
	Secret      string `json:"secret,omitempty"`
}

type webhookPayload struct {
	Config webhookConfig `json:"config"`
	Events []string      `json:"events"`
	Active bool          `json:"active"`
}

type webhookAPIResponse struct {
	ID     int           `json:"id"`
	Active bool          `json:"active"`
	Events []string      `json:"events"`
	Config webhookConfig `json:"config"`
}

func (c *APIClient) UpsertWebhook(ctx context.Context, repo, webhookURL, secret string, events []string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		events = []string{"issues", "issue_comment", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread", "pull_request"}
	}

	listPath := fmt.Sprintf("/repos/%s/%s/hooks", owner, repoName)
	listResp, err := c.do(ctx, http.MethodGet, listPath)
	if err != nil {
		return err
	}
	defer closeResponseBody(listResp)
	if listResp.StatusCode >= 300 {
		return fmt.Errorf("github list hooks failed (%d): %s", listResp.StatusCode, describeWebhookAuthFailure(listResp.StatusCode, []byte(readResponseBody(listResp.Body))))
	}

	var hooks []webhookAPIResponse
	if err := json.NewDecoder(listResp.Body).Decode(&hooks); err != nil {
		return fmt.Errorf("decode hooks: %w", err)
	}

	payload := webhookPayload{
		Config: webhookConfig{
			URL:         webhookURL,
			ContentType: "json",
			Secret:      secret,
		},
		Events: events,
		Active: true,
	}

	for _, h := range hooks {
		if strings.TrimSpace(h.Config.URL) != strings.TrimSpace(webhookURL) {
			continue
		}
		updatePath := fmt.Sprintf("/repos/%s/%s/hooks/%d", owner, repoName, h.ID)
		updateResp, err := doJSONRequest(ctx, c, http.MethodPatch, updatePath, payload)
		if err != nil {
			return err
		}
		defer closeResponseBody(updateResp)
		if updateResp.StatusCode >= 300 {
			return fmt.Errorf("github update hook failed (%d): %s", updateResp.StatusCode, readResponseBody(updateResp.Body))
		}
		return nil
	}

	createResp, err := doJSONRequest(ctx, c, http.MethodPost, listPath, payload)
	if err != nil {
		return err
	}
	defer closeResponseBody(createResp)
	if createResp.StatusCode >= 300 {
		return fmt.Errorf("github create hook failed (%d): %s", createResp.StatusCode, readResponseBody(createResp.Body))
	}
	return nil
}

func (c *APIClient) FindWebhookByURL(ctx context.Context, repo, webhookURL string) (*WebhookData, error) {
	hooks, err := c.listWebhooks(ctx, repo)
	if err != nil {
		return nil, err
	}
	want := strings.TrimSpace(webhookURL)
	for _, h := range hooks {
		if strings.TrimSpace(h.URL) == want {
			hook := h
			return &hook, nil
		}
	}
	return nil, nil
}

func (c *APIClient) DeleteWebhookByURL(ctx context.Context, repo, webhookURL string) (bool, error) {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	hook, err := c.FindWebhookByURL(ctx, repo, webhookURL)
	if err != nil {
		return false, err
	}
	if hook == nil {
		return false, nil
	}
	deletePath := fmt.Sprintf("/repos/%s/%s/hooks/%d", owner, repoName, hook.ID)
	resp, err := c.do(ctx, http.MethodDelete, deletePath)
	if err != nil {
		return false, err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return false, fmt.Errorf("github delete hook failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return true, nil
}

func (c *APIClient) listWebhooks(ctx context.Context, repo string) ([]WebhookData, error) {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}
	listPath := fmt.Sprintf("/repos/%s/%s/hooks", owner, repoName)
	listResp, err := c.do(ctx, http.MethodGet, listPath)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(listResp)
	if listResp.StatusCode >= 300 {
		return nil, fmt.Errorf("github list hooks failed (%d): %s", listResp.StatusCode, describeWebhookAuthFailure(listResp.StatusCode, []byte(readResponseBody(listResp.Body))))
	}
	var hooks []webhookAPIResponse
	if err := json.NewDecoder(listResp.Body).Decode(&hooks); err != nil {
		return nil, fmt.Errorf("decode hooks: %w", err)
	}
	out := make([]WebhookData, 0, len(hooks))
	for _, h := range hooks {
		out = append(out, WebhookData{
			ID:     h.ID,
			URL:    strings.TrimSpace(h.Config.URL),
			Active: h.Active,
			Events: h.Events,
		})
	}
	return out, nil
}

func describeWebhookAuthFailure(status int, body []byte) string {
	msg := strings.TrimSpace(string(body))
	if status != http.StatusForbidden {
		return msg
	}
	if !strings.Contains(strings.ToLower(msg), "resource not accessible by personal access token") {
		return msg
	}
	return msg + " (for fine-grained PATs, grant repository `Webhooks: Read and write`, plus `Issues: Read and write`, and ensure the target repo is selected)"
}
