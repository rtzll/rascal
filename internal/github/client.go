package github

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
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

const (
	ReactionPlusOne  = "+1"
	ReactionMinusOne = "-1"
	ReactionLaugh    = "laugh"
	ReactionConfused = "confused"
	ReactionHeart    = "heart"
	ReactionHooray   = "hooray"
	ReactionRocket   = "rocket"
	ReactionEyes     = "eyes"
)

type WebhookData struct {
	ID     int      `json:"id"`
	URL    string   `json:"url"`
	Active bool     `json:"active"`
	Events []string `json:"events"`
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
	defer closeResponseBody(resp)

	if resp.StatusCode >= 300 {
		return IssueData{}, fmt.Errorf("github get issue failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
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

func (c *APIClient) GetPullRequest(ctx context.Context, repo string, pullNumber int) (PullRequest, error) {
	if pullNumber <= 0 {
		return PullRequest{}, fmt.Errorf("pull number must be positive")
	}
	owner, name, err := splitRepo(repo)
	if err != nil {
		return PullRequest{}, err
	}

	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, name, pullNumber)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return PullRequest{}, err
	}
	defer closeResponseBody(resp)

	if resp.StatusCode >= 300 {
		return PullRequest{}, fmt.Errorf("github get pull request failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}

	var out PullRequest
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return PullRequest{}, fmt.Errorf("decode pull request: %w", err)
	}
	return out, nil
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
	defer closeResponseBody(resp)

	if resp.StatusCode == http.StatusOK {
		return nil
	}
	if resp.StatusCode != http.StatusNotFound {
		return fmt.Errorf("github get label failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
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
	defer closeResponseBody(createResp)
	if createResp.StatusCode >= 300 {
		return fmt.Errorf("github create label failed (%d): %s", createResp.StatusCode, readResponseBody(createResp.Body))
	}
	return nil
}

func (c *APIClient) LabelExists(ctx context.Context, repo, name string) (bool, error) {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return false, err
	}
	path := fmt.Sprintf("/repos/%s/%s/labels/%s", owner, repoName, url.PathEscape(name))
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}
	defer closeResponseBody(resp)
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		return false, fmt.Errorf("github get label failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
}

func (c *APIClient) AddIssueReaction(ctx context.Context, repo string, issueNumber int, content string) error {
	if issueNumber <= 0 {
		return fmt.Errorf("issue number must be positive")
	}
	content, err := validateReactionContent(content)
	if err != nil {
		return err
	}

	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/reactions", owner, repoName, issueNumber)
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"content": content})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github add issue reaction failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func (c *APIClient) RemoveIssueReactions(ctx context.Context, repo string, issueNumber int) error {
	if issueNumber <= 0 {
		return fmt.Errorf("issue number must be positive")
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	login, err := c.viewerLogin(ctx)
	if err != nil {
		return err
	}

	path := fmt.Sprintf("/repos/%s/%s/issues/%d/reactions?per_page=100", owner, repoName, issueNumber)
	resp, err := c.do(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github list issue reactions failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}

	var reactions []issueReaction
	if err := json.NewDecoder(resp.Body).Decode(&reactions); err != nil {
		return fmt.Errorf("decode issue reactions: %w", err)
	}
	for _, reaction := range reactions {
		if reaction.ID <= 0 || !strings.EqualFold(strings.TrimSpace(reaction.User.Login), login) {
			continue
		}
		deletePath := fmt.Sprintf("/repos/%s/%s/issues/%d/reactions/%d", owner, repoName, issueNumber, reaction.ID)
		deleteResp, err := c.do(ctx, http.MethodDelete, deletePath, nil)
		if err != nil {
			return err
		}
		body, readErr := io.ReadAll(deleteResp.Body)
		closeErr := deleteResp.Body.Close()
		if readErr != nil {
			return fmt.Errorf("read github delete issue reaction response: %w", readErr)
		}
		if closeErr != nil {
			return fmt.Errorf("close github delete issue reaction response: %w", closeErr)
		}
		if deleteResp.StatusCode >= 300 {
			return fmt.Errorf("github delete issue reaction failed (%d): %s", deleteResp.StatusCode, strings.TrimSpace(string(body)))
		}
	}
	return nil
}

func (c *APIClient) AddIssueCommentReaction(ctx context.Context, repo string, commentID int64, content string) error {
	if commentID <= 0 {
		return fmt.Errorf("comment id must be positive")
	}
	content, err := validateReactionContent(content)
	if err != nil {
		return err
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/comments/%d/reactions", owner, repoName, commentID)
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"content": content})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github add issue comment reaction failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func (c *APIClient) CreateIssueComment(ctx context.Context, repo string, issueNumber int, body string) error {
	if issueNumber <= 0 {
		return fmt.Errorf("issue number must be positive")
	}
	body = strings.TrimSpace(body)
	if body == "" {
		return fmt.Errorf("comment body is required")
	}

	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments", owner, repoName, issueNumber)
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"body": body})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github create issue comment failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func (c *APIClient) AddPullRequestReviewReaction(ctx context.Context, repo string, pullNumber int, reviewID int64, content string) error {
	if pullNumber <= 0 {
		return fmt.Errorf("pull number must be positive")
	}
	if reviewID <= 0 {
		return fmt.Errorf("review id must be positive")
	}
	content, err := validateReactionContent(content)
	if err != nil {
		return err
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d/reviews/%d/reactions", owner, repoName, pullNumber, reviewID)
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"content": content})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github add pull request review reaction failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func (c *APIClient) AddPullRequestReviewCommentReaction(ctx context.Context, repo string, commentID int64, content string) error {
	if commentID <= 0 {
		return fmt.Errorf("comment id must be positive")
	}
	content, err := validateReactionContent(content)
	if err != nil {
		return err
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	path := fmt.Sprintf("/repos/%s/%s/pulls/comments/%d/reactions", owner, repoName, commentID)
	resp, err := c.do(ctx, http.MethodPost, path, map[string]string{"content": content})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github add pull request review comment reaction failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func validateReactionContent(content string) (string, error) {
	content = strings.TrimSpace(content)
	switch content {
	case ReactionPlusOne, ReactionMinusOne, ReactionLaugh, ReactionConfused, ReactionHeart, ReactionHooray, ReactionRocket, ReactionEyes:
		return content, nil
	default:
		return "", fmt.Errorf("unsupported reaction content %q", content)
	}
}

func (c *APIClient) viewerLogin(ctx context.Context) (string, error) {
	resp, err := c.do(ctx, http.MethodGet, "/user", nil)
	if err != nil {
		return "", err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return "", fmt.Errorf("github get viewer failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
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

func (c *APIClient) UpsertWebhook(ctx context.Context, repo, webhookURL, secret string, events []string) error {
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return err
	}
	if len(events) == 0 {
		events = []string{"issues", "issue_comment", "pull_request_review", "pull_request_review_comment", "pull_request_review_thread", "pull_request"}
	}

	listPath := fmt.Sprintf("/repos/%s/%s/hooks", owner, repoName)
	listResp, err := c.do(ctx, http.MethodGet, listPath, nil)
	if err != nil {
		return err
	}
	defer closeResponseBody(listResp)
	if listResp.StatusCode >= 300 {
		return fmt.Errorf("github list hooks failed (%d): %s", listResp.StatusCode, describeWebhookAuthFailure(listResp.StatusCode, []byte(readResponseBody(listResp.Body))))
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
		defer closeResponseBody(updateResp)
		if updateResp.StatusCode >= 300 {
			return fmt.Errorf("github update hook failed (%d): %s", updateResp.StatusCode, readResponseBody(updateResp.Body))
		}
		return nil
	}

	createResp, err := c.do(ctx, http.MethodPost, listPath, payload)
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
	resp, err := c.do(ctx, http.MethodDelete, deletePath, nil)
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
	listResp, err := c.do(ctx, http.MethodGet, listPath, nil)
	if err != nil {
		return nil, err
	}
	defer closeResponseBody(listResp)
	if listResp.StatusCode >= 300 {
		return nil, fmt.Errorf("github list hooks failed (%d): %s", listResp.StatusCode, describeWebhookAuthFailure(listResp.StatusCode, []byte(readResponseBody(listResp.Body))))
	}
	var hooks []struct {
		ID     int      `json:"id"`
		Active bool     `json:"active"`
		Events []string `json:"events"`
		Config struct {
			URL string `json:"url"`
		} `json:"config"`
	}
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

func closeResponseBody(resp *http.Response) {
	if resp == nil || resp.Body == nil {
		return
	}
	if err := resp.Body.Close(); err != nil {
		log.Printf("close github response body: %v", err)
	}
}

func readResponseBody(body io.Reader) string {
	data, err := io.ReadAll(body)
	if err != nil {
		return fmt.Sprintf("failed to read response body: %v", err)
	}
	return strings.TrimSpace(string(data))
}
