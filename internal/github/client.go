package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const defaultAPIURL = "https://api.github.com"

type APIClient struct {
	token   string
	baseURL string
	http    *http.Client
}

type issueReaction struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

func NewAPIClient(token string) *APIClient {
	return &APIClient{
		token:   strings.TrimSpace(token),
		baseURL: defaultAPIURL,
		http:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *APIClient) viewerLogin(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("github get viewer failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out struct {
		Login string `json:"login"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", fmt.Errorf("decode viewer: %w", err)
	}
	login := strings.TrimSpace(out.Login)
	if login == "" {
		return "", fmt.Errorf("github viewer login is empty")
	}
	return login, nil
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
