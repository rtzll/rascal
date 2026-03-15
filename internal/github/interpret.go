package github

import (
	"fmt"
	"strings"
)

func IssueHasLabel(labels []Label, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	for _, label := range labels {
		if strings.EqualFold(strings.TrimSpace(label.Name), name) {
			return true
		}
	}
	return false
}

func IssueCommentBodyChanged(ev IssueCommentEvent) bool {
	if ev.Changes.Body == nil {
		return false
	}
	newBody := strings.TrimSpace(ev.Comment.Body)
	oldBody := strings.TrimSpace(ev.Changes.Body.From)
	return newBody != oldBody
}

func ReviewCommentBodyChanged(ev PullRequestReviewCommentEvent) bool {
	if ev.Changes.Body == nil {
		return false
	}
	newBody := strings.TrimSpace(ev.Comment.Body)
	oldBody := strings.TrimSpace(ev.Changes.Body.From)
	return newBody != oldBody
}

func ReviewThreadContext(thread ReviewThread) string {
	for i := len(thread.Comments) - 1; i >= 0; i-- {
		body := strings.TrimSpace(thread.Comments[i].Body)
		if body == "" {
			continue
		}
		if location := FormatReviewCommentLocation(thread.Path, thread.StartLine, thread.Line); location != "" {
			return fmt.Sprintf("%s\n\nThread location: %s", body, location)
		}
		return body
	}
	if location := FormatReviewCommentLocation(thread.Path, thread.StartLine, thread.Line); location != "" {
		return fmt.Sprintf("review thread marked unresolved at %s", location)
	}
	return "review thread marked unresolved"
}

func ReviewThreadSourceText(thread ReviewThread) string {
	var parts []string
	if location := FormatReviewCommentLocation(thread.Path, thread.StartLine, thread.Line); location != "" {
		parts = append(parts, fmt.Sprintf("Thread location: %s", location))
	}
	for _, comment := range thread.Comments {
		body := strings.TrimSpace(comment.Body)
		if body == "" {
			continue
		}
		parts = append(parts, body)
	}
	return strings.TrimSpace(strings.Join(parts, "\n\n"))
}

func IssueTaskFromIssue(title, body string) string {
	title = strings.TrimSpace(title)
	body = strings.TrimSpace(body)
	if body == "" {
		return title
	}
	return fmt.Sprintf("%s\n\n%s", title, body)
}

func FormatReviewCommentLocation(path string, startLine, line *int) string {
	path = strings.TrimSpace(path)
	if line != nil && *line > 0 {
		if startLine != nil && *startLine > 0 && *startLine != *line {
			if path == "" {
				return fmt.Sprintf("lines %d-%d", *startLine, *line)
			}
			return fmt.Sprintf("%s:%d-%d", path, *startLine, *line)
		}
		if path == "" {
			return fmt.Sprintf("line %d", *line)
		}
		return fmt.Sprintf("%s:%d", path, *line)
	}
	return path
}

func IsAutomationComment(body string, markers ...string) bool {
	trimmed := strings.TrimSpace(body)
	if trimmed == "" {
		return false
	}
	for _, marker := range markers {
		if strings.TrimSpace(marker) != "" && strings.Contains(trimmed, marker) {
			return true
		}
	}
	legacy := strings.ToLower(trimmed)
	return strings.Contains(legacy, "rascal run `") && strings.Contains(legacy, "completed in ")
}
