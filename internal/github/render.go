package github

import (
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/runsummary"
)

func PrefixMarker(marker, body string) string {
	body = strings.TrimSpace(body)
	if strings.TrimSpace(marker) == "" {
		return body
	}
	if body == "" {
		return marker
	}
	return marker + "\n\n" + body
}

func RenderStartComment(marker string, input runsummary.StartCommentInput) string {
	return PrefixMarker(marker, runsummary.BuildStartComment(input))
}

func RenderCompletionComment(marker string, input runsummary.CompletionCommentInput) (string, error) {
	body, err := runsummary.BuildCompletionComment(input)
	if err != nil {
		return "", fmt.Errorf("build completion comment: %w", err)
	}
	return PrefixMarker(marker, body), nil
}

func RenderFailureComment(marker, header, retryAt, reason, details string) string {
	parts := []string{strings.TrimSpace(header)}
	if strings.TrimSpace(retryAt) != "" {
		parts = append(parts, fmt.Sprintf("The provider said to try again at %s.", strings.TrimSpace(retryAt)))
	}
	if strings.TrimSpace(reason) != "" {
		parts = append(parts, fmt.Sprintf("Reason: %s", strings.TrimSpace(reason)))
	}
	if strings.TrimSpace(details) != "" {
		parts = append(parts, "<details><summary>Failure Details</summary>\n\n```text\n"+strings.TrimSpace(details)+"\n```\n\n</details>")
	}
	return PrefixMarker(marker, strings.Join(parts, "\n\n"))
}
