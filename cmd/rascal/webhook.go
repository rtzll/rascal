package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"sort"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/config"
	ghapi "github.com/rtzll/rascal/internal/github"
	"github.com/spf13/cobra"
)

const (
	webhookTestDefaultEvent          = "issues"
	webhookTestResponseSnippetLimit  = 200
	webhookTestResponseBodyReadLimit = 64 * 1024
)

var webhookTestEvents = []string{
	"issues",
	"issue_comment",
	"pull_request_review",
	"pull_request_review_comment",
	"pull_request",
}

type webhookTestInput struct {
	ServerURL     string
	Repo          string
	Event         string
	WebhookSecret string
}

type webhookTestPayloadValue struct {
	raw        []byte
	jsonOutput bool
}

type webhookTestRequestOutput struct {
	Method  string                  `json:"method"`
	URL     string                  `json:"url"`
	Headers map[string]string       `json:"headers"`
	Payload webhookTestPayloadValue `json:"payload"`
}

type webhookTestResponseOutput struct {
	Status  string            `json:"status"`
	Headers map[string]string `json:"headers"`
	Body    string            `json:"body"`
}

type webhookTestOutput struct {
	WebhookURL      string                     `json:"webhook_url"`
	Repo            string                     `json:"repo"`
	Event           string                     `json:"event"`
	DryRun          bool                       `json:"dry_run"`
	Signature       string                     `json:"signature"`
	DeliveryID      string                     `json:"delivery_id"`
	Payload         webhookTestPayloadValue    `json:"payload"`
	Status          int                        `json:"status,omitempty"`
	StatusText      string                     `json:"status_text,omitempty"`
	RequestID       string                     `json:"request_id,omitempty"`
	ResponseSnippet string                     `json:"response_snippet,omitempty"`
	Request         *webhookTestRequestOutput  `json:"request,omitempty"`
	Response        *webhookTestResponseOutput `json:"response,omitempty"`
}

func (v webhookTestPayloadValue) MarshalJSON() ([]byte, error) {
	if v.jsonOutput {
		if len(v.raw) == 0 {
			return []byte("null"), nil
		}
		return append([]byte(nil), v.raw...), nil
	}
	data, err := json.Marshal(string(v.raw))
	if err != nil {
		return nil, fmt.Errorf("marshal webhook payload text: %w", err)
	}
	return data, nil
}

func (v webhookTestPayloadValue) MarshalText() ([]byte, error) {
	return append([]byte(nil), v.raw...), nil
}

func (a *app) newWebhookCmd() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "webhook",
		Short: "Webhook helpers",
		RunE: func(cmd *cobra.Command, _ []string) error {
			return cmd.Help()
		},
	}
	cmd.AddCommand(a.newWebhookTestCmd())
	return cmd
}

func (a *app) newWebhookTestCmd() *cobra.Command {
	var (
		webhookSecret string
		repo          string
		event         string
		dryRun        bool
		verbose       bool
	)

	cmd := &cobra.Command{
		Use:   "test",
		Short: "Send a synthetic GitHub webhook to the server",
		RunE: func(_ *cobra.Command, _ []string) error {
			resolved, err := resolveWebhookTestInput(webhookTestInput{
				ServerURL:     a.cfg.ServerURL,
				Repo:          repo,
				Event:         event,
				WebhookSecret: webhookSecret,
			}, a.cfg)
			if err != nil {
				return err
			}

			payload, err := buildWebhookTestPayload(resolved.Event, resolved.Repo)
			if err != nil {
				return &cliError{
					Code:    exitInput,
					Message: err.Error(),
					Hint:    fmt.Sprintf("use --event %s", strings.Join(webhookTestEvents, "|")),
				}
			}

			signature := ghapi.SignatureSHA256([]byte(resolved.WebhookSecret), payload)
			if signature == "" {
				return &cliError{Code: exitInput, Message: "failed to sign payload", Hint: "check --webhook-secret"}
			}

			webhookURL := resolved.ServerURL + "/v1/webhooks/github"
			deliveryID := webhookTestDeliveryID(dryRun)

			headers := http.Header{}
			headers.Set("Content-Type", "application/json")
			headers.Set("User-Agent", "rascal-cli")
			headers.Set("X-GitHub-Event", resolved.Event)
			headers.Set("X-GitHub-Delivery", deliveryID)
			headers.Set("X-Hub-Signature-256", signature)

			payloadValue := webhookTestPayloadValue{
				raw:        payload,
				jsonOutput: a.output == "json",
			}

			out := webhookTestOutput{
				WebhookURL: webhookURL,
				Repo:       resolved.Repo,
				Event:      resolved.Event,
				DryRun:     dryRun,
				Signature:  signature,
				DeliveryID: deliveryID,
				Payload:    payloadValue,
			}

			if dryRun {
				if verbose {
					out.Request = &webhookTestRequestOutput{
						Method:  http.MethodPost,
						URL:     webhookURL,
						Headers: headerMap(headers),
						Payload: payloadValue,
					}
				}
				return a.emit(out, func() error {
					return renderWebhookTestTable(a, webhookTestTableInput{
						WebhookURL: webhookURL,
						Repo:       resolved.Repo,
						Event:      resolved.Event,
						Signature:  signature,
						DeliveryID: deliveryID,
						Payload:    payload,
						Verbose:    verbose,
						DryRun:     true,
						Headers:    headers,
					})
				})
			}

			client := &http.Client{Timeout: 30 * time.Second}
			req, err := http.NewRequest(http.MethodPost, webhookURL, bytes.NewReader(payload))
			if err != nil {
				return &cliError{Code: exitRuntime, Message: "failed to build webhook request", Cause: err}
			}
			for key, values := range headers {
				for _, value := range values {
					req.Header.Add(key, value)
				}
			}

			resp, err := client.Do(req)
			if err != nil {
				return &cliError{Code: exitServer, Message: "webhook request failed", Cause: err}
			}
			defer closeWithLog("close webhook test response body", resp.Body)

			body, err := io.ReadAll(io.LimitReader(resp.Body, webhookTestResponseBodyReadLimit))
			if err != nil {
				return &cliError{Code: exitServer, Message: "failed to read webhook response", Cause: err}
			}
			reqID := strings.TrimSpace(resp.Header.Get("X-Request-ID"))
			snippet := webhookResponseSnippet(body, webhookTestResponseSnippetLimit)

			out.Status = resp.StatusCode
			out.StatusText = resp.Status
			if reqID != "" {
				out.RequestID = reqID
			}
			if snippet != "" {
				out.ResponseSnippet = snippet
			}
			if verbose {
				out.Request = &webhookTestRequestOutput{
					Method:  http.MethodPost,
					URL:     webhookURL,
					Headers: headerMap(headers),
					Payload: payloadValue,
				}
				out.Response = &webhookTestResponseOutput{
					Status:  resp.Status,
					Headers: headerMap(resp.Header),
					Body:    strings.TrimSpace(string(body)),
				}
			}

			err = a.emit(out, func() error {
				return renderWebhookTestTable(a, webhookTestTableInput{
					WebhookURL: webhookURL,
					Repo:       resolved.Repo,
					Event:      resolved.Event,
					Signature:  signature,
					DeliveryID: deliveryID,
					Payload:    payload,
					Verbose:    verbose,
					DryRun:     false,
					Headers:    headers,
					Response: &webhookTestTableResponse{
						Status:    resp.Status,
						RequestID: reqID,
						Snippet:   snippet,
						Headers:   resp.Header,
						Body:      body,
					},
				})
			})
			if err != nil {
				return err
			}
			if resp.StatusCode >= 300 {
				return &cliError{Code: exitServer, Message: fmt.Sprintf("webhook response status %d", resp.StatusCode), RequestID: reqID}
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&webhookSecret, "webhook-secret", "", "GitHub webhook secret (or RASCAL_GITHUB_WEBHOOK_SECRET)")
	cmd.Flags().StringVar(&repo, "repo", "", "repository in OWNER/REPO form")
	cmd.Flags().StringVar(&event, "event", webhookTestDefaultEvent, "event template: issues|issue_comment|pull_request_review|pull_request_review_comment|pull_request")
	cmd.Flags().BoolVar(&dryRun, "dry-run", false, "print payload/signature without sending")
	cmd.Flags().BoolVar(&verbose, "verbose", false, "print full request/response")
	return cmd
}

type webhookTestTableInput struct {
	WebhookURL string
	Repo       string
	Event      string
	Signature  string
	DeliveryID string
	Payload    []byte
	Headers    http.Header
	Verbose    bool
	DryRun     bool
	Response   *webhookTestTableResponse
}

type webhookTestTableResponse struct {
	Status    string
	RequestID string
	Snippet   string
	Headers   http.Header
	Body      []byte
}

func renderWebhookTestTable(a *app, input webhookTestTableInput) error {
	if a.quiet {
		return nil
	}
	fmt.Printf("webhook: %s\n", input.WebhookURL)
	fmt.Printf("repo: %s\n", input.Repo)
	fmt.Printf("event: %s\n", input.Event)
	fmt.Printf("signature: %s\n", input.Signature)
	fmt.Printf("delivery_id: %s\n", input.DeliveryID)
	if input.DryRun {
		fmt.Printf("dry_run: true\n")
	} else if input.Response != nil {
		fmt.Printf("status: %s\n", input.Response.Status)
		if input.Response.RequestID != "" {
			fmt.Printf("request_id: %s\n", input.Response.RequestID)
		}
		if input.Response.Snippet != "" {
			fmt.Printf("response: %s\n", input.Response.Snippet)
		}
	}
	if input.DryRun {
		fmt.Printf("payload:\n%s\n", input.Payload)
	}
	if input.Verbose {
		fmt.Printf("\nrequest:\n")
		fmt.Printf("%s %s\n", http.MethodPost, input.WebhookURL)
		for _, line := range formatHeaderLines(input.Headers) {
			fmt.Println(line)
		}
		fmt.Printf("payload:\n%s\n", input.Payload)
		if input.Response != nil {
			fmt.Printf("\nresponse:\n")
			fmt.Printf("status: %s\n", input.Response.Status)
			for _, line := range formatHeaderLines(input.Response.Headers) {
				fmt.Println(line)
			}
			if len(input.Response.Body) > 0 {
				fmt.Printf("body:\n%s\n", strings.TrimSpace(string(input.Response.Body)))
			}
		}
	}
	return nil
}

func resolveWebhookTestInput(in webhookTestInput, cfg config.ClientConfig) (webhookTestInput, error) {
	out := webhookTestInput{}
	out.ServerURL = strings.TrimRight(strings.TrimSpace(firstNonEmpty(in.ServerURL, cfg.ServerURL)), "/")
	out.Repo = strings.TrimSpace(firstNonEmpty(in.Repo, cfg.DefaultRepo))
	out.Event = strings.ToLower(strings.TrimSpace(in.Event))
	if out.Event == "" {
		out.Event = webhookTestDefaultEvent
	}
	out.WebhookSecret = firstNonEmpty(strings.TrimSpace(in.WebhookSecret), strings.TrimSpace(os.Getenv("RASCAL_GITHUB_WEBHOOK_SECRET")))

	if out.ServerURL == "" {
		return out, &cliError{Code: exitInput, Message: "missing server url", Hint: "pass --server-url or set RASCAL_SERVER_URL"}
	}
	if out.Repo == "" {
		return out, &cliError{Code: exitInput, Message: "repo is required", Hint: "pass --repo or set --default-repo"}
	}
	if out.WebhookSecret == "" {
		return out, &cliError{Code: exitInput, Message: "missing webhook secret", Hint: "pass --webhook-secret or set RASCAL_GITHUB_WEBHOOK_SECRET"}
	}

	return out, nil
}

func buildWebhookTestPayload(event, repo string) ([]byte, error) {
	switch event {
	case "issues":
		return marshalWebhookTestPayload(buildIssuesWebhookTestEvent(repo))
	case "issue_comment":
		return marshalWebhookTestPayload(buildIssueCommentWebhookTestEvent(repo))
	case "pull_request_review":
		return marshalWebhookTestPayload(buildPullRequestReviewWebhookTestEvent(repo))
	case "pull_request_review_comment":
		return marshalWebhookTestPayload(buildPullRequestReviewCommentWebhookTestEvent(repo))
	case "pull_request":
		return marshalWebhookTestPayload(buildPullRequestWebhookTestEvent(repo))
	default:
		return nil, fmt.Errorf("build webhook test event: unsupported event %q", event)
	}
}

func marshalWebhookTestPayload[T any](payload T) ([]byte, error) {
	data, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal webhook test payload: %w", err)
	}
	return data, nil
}

func buildIssuesWebhookTestEvent(repo string) ghapi.IssuesEvent {
	const (
		issueNumber = 123
		actorLogin  = "rascal-tester"
	)
	return ghapi.IssuesEvent{
		Action: "labeled",
		Label:  ghapi.Label{Name: "rascal"},
		Issue: ghapi.Issue{
			Number:      issueNumber,
			Title:       "Rascal webhook test issue",
			Body:        "Synthetic webhook payload from rascal webhook test.",
			PullRequest: nil,
		},
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: actorLogin},
	}
}

func buildIssueCommentWebhookTestEvent(repo string) ghapi.IssueCommentEvent {
	const (
		issueNumber = 123
		commentID   = 456
		actorLogin  = "rascal-tester"
	)
	return ghapi.IssueCommentEvent{
		Action: "created",
		Issue: ghapi.Issue{
			Number:      issueNumber,
			Title:       "Rascal webhook test PR",
			Body:        "Synthetic PR for webhook test.",
			PullRequest: &ghapi.PullRequestRef{URL: "https://example.com/pull/123"},
		},
		Comment: ghapi.Comment{
			ID:   commentID,
			Body: "Synthetic comment from rascal webhook test.",
			User: ghapi.User{Login: actorLogin},
		},
		Repository: ghapi.Repository{FullName: repo},
		Sender:     ghapi.User{Login: actorLogin},
	}
}

func buildPullRequestReviewWebhookTestEvent(repo string) ghapi.PullRequestReviewEvent {
	const (
		reviewID   = 789
		actorLogin = "rascal-tester"
	)
	pr := buildWebhookTestPullRequest()
	return ghapi.PullRequestReviewEvent{
		Action: "submitted",
		Review: ghapi.Review{
			ID:    reviewID,
			Body:  "Synthetic review from rascal webhook test.",
			State: "commented",
			User:  ghapi.User{Login: actorLogin},
		},
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: actorLogin},
	}
}

func buildPullRequestReviewCommentWebhookTestEvent(repo string) ghapi.PullRequestReviewCommentEvent {
	const (
		commentID  = 456
		actorLogin = "rascal-tester"
	)
	pr := buildWebhookTestPullRequest()
	line := 42
	startLine := 40
	return ghapi.PullRequestReviewCommentEvent{
		Action: "created",
		Comment: ghapi.ReviewComment{
			ID:        commentID,
			Body:      "Synthetic inline review comment from rascal webhook test.",
			Path:      "cmd/rascald/main.go",
			Line:      &line,
			StartLine: &startLine,
			User:      ghapi.User{Login: actorLogin},
		},
		PullRequest: pr,
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: actorLogin},
	}
}

func buildPullRequestWebhookTestEvent(repo string) ghapi.PullRequestEvent {
	const actorLogin = "rascal-tester"
	return ghapi.PullRequestEvent{
		Action:      "opened",
		PullRequest: buildWebhookTestPullRequest(),
		Repository:  ghapi.Repository{FullName: repo},
		Sender:      ghapi.User{Login: actorLogin},
	}
}

func buildWebhookTestPullRequest() ghapi.PullRequest {
	const (
		prNumber   = 123
		baseBranch = "main"
		headBranch = "rascal/webhook-test"
	)
	pr := ghapi.PullRequest{Number: prNumber, Merged: false}
	pr.Base.Ref = baseBranch
	pr.Head.Ref = headBranch
	return pr
}

func webhookTestDeliveryID(dryRun bool) string {
	if dryRun {
		return "rascal-test-delivery"
	}
	return fmt.Sprintf("rascal-test-%d", time.Now().UTC().UnixNano())
}

func webhookResponseSnippet(body []byte, limit int) string {
	if limit <= 0 {
		limit = webhookTestResponseSnippetLimit
	}
	trimmed := strings.TrimSpace(string(body))
	if trimmed == "" {
		return ""
	}
	if len(trimmed) <= limit {
		return trimmed
	}
	return trimmed[:limit] + "..."
}

func headerMap(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for key, values := range h {
		out[key] = strings.Join(values, ", ")
	}
	return out
}

func formatHeaderLines(h http.Header) []string {
	keys := make([]string, 0, len(h))
	for key := range h {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	lines := make([]string, 0, len(keys))
	for _, key := range keys {
		lines = append(lines, fmt.Sprintf("%s: %s", key, strings.Join(h[key], ", ")))
	}
	return lines
}
