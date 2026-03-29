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

type issueReaction struct {
	ID   int64 `json:"id"`
	User struct {
		Login string `json:"login"`
	} `json:"user"`
}

type labelCreateRequest struct {
	Name        string `json:"name"`
	Color       string `json:"color"`
	Description string `json:"description"`
}

type reactionRequest struct {
	Content string `json:"content"`
}

type issueCommentRequest struct {
	Body string `json:"body"`
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
	resp, err := c.do(ctx, http.MethodGet, path)
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
	resp, err := c.do(ctx, http.MethodGet, path)
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
	resp, err := c.do(ctx, http.MethodGet, path)
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

	payload := labelCreateRequest{
		Name:        name,
		Color:       color,
		Description: description,
	}
	createPath := fmt.Sprintf("/repos/%s/%s/labels", owner, repoName)
	createResp, err := doJSONRequest(ctx, c, http.MethodPost, createPath, payload)
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
	resp, err := c.do(ctx, http.MethodGet, path)
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
	resp, err := doJSONRequest(ctx, c, http.MethodPost, path, reactionRequest{Content: content})
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
	resp, err := c.do(ctx, http.MethodGet, path)
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
		deleteResp, err := c.do(ctx, http.MethodDelete, deletePath)
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
	resp, err := doJSONRequest(ctx, c, http.MethodPost, path, reactionRequest{Content: content})
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
	resp, err := doJSONRequest(ctx, c, http.MethodPost, path, issueCommentRequest{Body: body})
	if err != nil {
		return err
	}
	defer closeResponseBody(resp)
	if resp.StatusCode >= 300 {
		return fmt.Errorf("github create issue comment failed (%d): %s", resp.StatusCode, readResponseBody(resp.Body))
	}
	return nil
}

func (c *APIClient) ListIssueComments(ctx context.Context, repo string, issueNumber int) ([]Comment, error) {
	if issueNumber <= 0 {
		return nil, fmt.Errorf("issue number must be positive")
	}
	owner, repoName, err := splitRepo(repo)
	if err != nil {
		return nil, err
	}

	comments := make([]Comment, 0, 16)
	for page := 1; ; page++ {
		path := fmt.Sprintf("/repos/%s/%s/issues/%d/comments?per_page=100&page=%d", owner, repoName, issueNumber, page)
		resp, err := c.do(ctx, http.MethodGet, path)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 300 {
			body := readResponseBody(resp.Body)
			closeResponseBody(resp)
			return nil, fmt.Errorf("github list issue comments failed (%d): %s", resp.StatusCode, body)
		}

		var pageComments []Comment
		if err := json.NewDecoder(resp.Body).Decode(&pageComments); err != nil {
			closeResponseBody(resp)
			return nil, fmt.Errorf("decode issue comments: %w", err)
		}
		closeResponseBody(resp)
		comments = append(comments, pageComments...)
		if len(pageComments) < 100 {
			break
		}
	}
	return comments, nil
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
	resp, err := doJSONRequest(ctx, c, http.MethodPost, path, reactionRequest{Content: content})
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
	resp, err := doJSONRequest(ctx, c, http.MethodPost, path, reactionRequest{Content: content})
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
	resp, err := c.do(ctx, http.MethodGet, "/user")
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
