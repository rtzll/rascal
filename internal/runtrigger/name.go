package runtrigger

import (
	"fmt"
	"strings"
)

type Name string

const (
	NameCLI             Name = "cli"
	NameCampaign        Name = "campaign"
	NameRetry           Name = "retry"
	NameIssueAPI        Name = "issue_api"
	NameIssueLabel      Name = "issue_label"
	NameIssueEdited     Name = "issue_edited"
	NameIssueReopened   Name = "issue_reopened"
	NamePRComment       Name = "pr_comment"
	NamePRReview        Name = "pr_review"
	NamePRReviewComment Name = "pr_review_comment"
	NamePRReviewThread  Name = "pr_review_thread"
)

func Normalize(raw string) Name {
	return Name(strings.ToLower(strings.TrimSpace(raw)))
}

func Parse(raw string) (Name, error) {
	name := Normalize(raw)
	if !name.IsKnown() {
		return "", fmt.Errorf("unknown workflow trigger %q", raw)
	}
	return name, nil
}

func ParseOrDefault(raw string, fallback Name) (Name, error) {
	if Normalize(raw) == "" {
		return fallback, nil
	}
	return Parse(raw)
}

func (n Name) String() string {
	return string(n)
}

func (n Name) IsKnown() bool {
	switch Normalize(n.String()) {
	case NameCLI,
		NameCampaign,
		NameRetry,
		NameIssueAPI,
		NameIssueLabel,
		NameIssueEdited,
		NameIssueReopened,
		NamePRComment,
		NamePRReview,
		NamePRReviewComment,
		NamePRReviewThread:
		return true
	default:
		return false
	}
}

func (n Name) IsComment() bool {
	switch Normalize(n.String()) {
	case NamePRComment, NamePRReview, NamePRReviewComment, NamePRReviewThread:
		return true
	default:
		return false
	}
}

func (n Name) IsIssue() bool {
	switch Normalize(n.String()) {
	case NameIssueAPI, NameIssueLabel, NameIssueEdited, NameIssueReopened:
		return true
	default:
		return false
	}
}

func (n Name) EnablesPROnlySession() bool {
	switch Normalize(n.String()) {
	case NamePRComment, NamePRReview, NamePRReviewComment, NamePRReviewThread, NameRetry, NameIssueEdited:
		return true
	default:
		return false
	}
}
