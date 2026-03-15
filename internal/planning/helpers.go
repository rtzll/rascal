package planning

import (
	"fmt"
	"strings"

	ghapi "github.com/rtzll/rascal/internal/github"
)

func ManualPromptSource(text string) Source {
	return Source{
		Kind:  SourceKindManualPrompt,
		Label: "Manual prompt",
		Text:  strings.TrimSpace(text),
	}
}

func IssueSource(title, body, url string) Source {
	return Source{
		Kind:  SourceKindGitHubIssue,
		Label: "GitHub issue",
		URL:   strings.TrimSpace(url),
		Text:  strings.TrimSpace(ghapi.IssueTaskFromIssue(title, body)),
	}
}

func ReferenceSource(label, text string) Source {
	return Source{
		Kind:  SourceKindReference,
		Label: strings.TrimSpace(label),
		Text:  strings.TrimSpace(text),
	}
}

func ContextSource(label, text string) Source {
	return Source{
		Kind:  SourceKindTriggerContext,
		Label: strings.TrimSpace(label),
		Text:  strings.TrimSpace(text),
	}
}

func PRCommentSource(body, author string) Source {
	return Source{
		Kind:   SourceKindGitHubPRComment,
		Label:  "PR comment",
		Author: strings.TrimSpace(author),
		Text:   strings.TrimSpace(body),
	}
}

func PRReviewSource(body, state, author string) Source {
	body = strings.TrimSpace(body)
	if body == "" && strings.TrimSpace(state) != "" {
		body = fmt.Sprintf("Review state: %s", strings.TrimSpace(state))
	}
	return Source{
		Kind:   SourceKindGitHubPRReview,
		Label:  "PR review",
		Author: strings.TrimSpace(author),
		Text:   body,
	}
}

func PRReviewCommentSource(body, location, author string) Source {
	return Source{
		Kind:     SourceKindGitHubPRReviewNote,
		Label:    "PR review comment",
		Author:   strings.TrimSpace(author),
		Location: strings.TrimSpace(location),
		Text:     strings.TrimSpace(body),
	}
}

func PRReviewThreadSource(text, location string) Source {
	return Source{
		Kind:     SourceKindGitHubPRThread,
		Label:    "PR review thread",
		Location: strings.TrimSpace(location),
		Text:     strings.TrimSpace(text),
	}
}
