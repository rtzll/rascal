package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const defaultAPIURL = "https://api.github.com"

type APIClient struct {
	token   string
	baseURL string
	http    *http.Client
}

type IssueData struct {
	Number  int
	Title   string
	Body    string
	HTMLURL string
}

func NewAPIClient(token string) *APIClient {
	return &APIClient{
		token:   strings.TrimSpace(token),
		baseURL: defaultAPIURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *APIClient) GetIssue(ctx context.Context, repo string, issueNumber int) (IssueData, error) {
	if issueNumber <= 0 {
		return IssueData{}, fmt.Errorf("issue number must be positive")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return IssueData{}, err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d", owner, name, issueNumber)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return IssueData{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return IssueData{}, fmt.Errorf("github get issue failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	var out struct {
		Number  int    `json:"number"`
		Title   string `json:"title"`
		Body    string `json:"body"`
		HTMLURL string `json:"html_url"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return IssueData{}, fmt.Errorf("decode issue: %w", err)
	}

	return IssueData{
		Number:  out.Number,
		Title:   out.Title,
		Body:    out.Body,
		HTMLURL: out.HTMLURL,
	}, nil
}

func (c *APIClient) EnsureLabel(ctx context.Context, repo, name, color, description string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if color == "" {
		color = "0e8a16"
	}

	path := fmt.Sprintf("/repos/%s/%s/labels/%s", owner, repoName, url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github get label failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	payload := map[string]string{
		"name":        name,
		"color":       color,
		"description": description,
	}
	createPath := fmt.Sprintf("/repos/%s/%s/labels", owner, repoName)
	createResp, err := c.do(ctx, http.MethodPost, createPath, payload)
	if err != nil {
		return err
	}
	defer createResp.Body.Close()
	if createResp.StatusCode >= 300 {
		body, _ := io.ReadAll(createResp.Body)
		return fmt.Errorf("github create label failed (%d): %s", createResp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *APIClient) UpsertWebhook(ctx context.Context, repo, webhookURL, secret string, events []string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		events = []string{"issues", "issue_comment", "pull_request_review", "pull_request"}
	}

	listPath := fmt.Sprintf("/repos/%s/%s/hooks", owner, repoName)
	listResp, err := c.do(ctx, http.MethodGet, listPath, nil)
	if err != nil {
		return err
	}
	defer listResp.Body.Close()
	if listResp.StatusCode >= 300 {
		body, _ := io.ReadAll(listResp.Body)
		return fmt.Errorf("github list hooks failed (%d): %s", listResp.StatusCode, strings.TrimSpace(string(body)))
	}

	var hooks []struct {
		ID     int `json:"id"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
	if err := json.NewDecoder(listResp.Body).Decode(&hooks); err != nil {
		return fmt.Errorf("decode hooks: %w", err)
	}

	payload := map[string]any{
		"config": map[string]string{
			"url":          webhookURL,
			"content_type": "json",
			"secret":       secret,
		},
		"events": events,
		"active": true,
	}

	for _, h := range hooks {
		if strings.TrimSpace(h.Config.URL) != strings.TrimSpace(webhookURL) {
			continue
		}
		updatePath := fmt.Sprintf("/repos/%s/%s/hooks/%d", owner, repoName, h.ID)
		updateResp, err := c.do(ctx, http.MethodPatch, updatePath, payload)
		if err != nil {
			return err
		}
		defer updateResp.Body.Close()
		if updateResp.StatusCode >= 300 {
			body, _ := io.ReadAll(updateResp.Body)
			return fmt.Errorf("github update hook failed (%d): %s", updateResp.StatusCode, strings.TrimSpace(string(body)))
		}
		return nil
	}

	createResp, err := c.do(ctx, http.MethodPost, listPath, payload)
	if err != nil {
		return err
	}
	defer createResp.Body.Close()
	if createResp.StatusCode >= 300 {
		body, _ := io.ReadAll(createResp.Body)
		return fmt.Errorf("github create hook failed (%d): %s", createResp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *APIClient) do(ctx context.Context, method, path string, payload any) (*http.Response, error) {
	var body io.Reader
	if payload != nil {
		data, err := json.Marshal(payload)
		if err != nil {
			return nil, fmt.Errorf("encode payload: %w", err)
		}
		body = bytes.NewReader(data)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, fmt.Errorf("build github request: %w", err)
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github request failed: %w", err)
	}
	return resp, nil
}

func splitRepo(repo string) (owner, name string, err error) {
	parts := strings.Split(strings.TrimSpace(repo), "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("invalid repo %q, expected OWNER/REPO", repo)
	}
	return parts[0], parts[1], nil
}
