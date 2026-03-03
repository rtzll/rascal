package github

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
)

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
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
		return true, nil
	case http.StatusNotFound:
		return false, nil
	default:
		body, _ := io.ReadAll(resp.Body)
		return false, fmt.Errorf("github get label failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github add issue reaction failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github add issue comment reaction failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github add pull request review reaction failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("github add pull request review comment reaction failed (%d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
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
