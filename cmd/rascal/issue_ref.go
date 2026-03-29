package main

import (
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/state"
)

type issueRef struct {
	Repo        string
	IssueNumber int
}

func parseIssueRef(input string) (issueRef, error) {
	input = strings.TrimSpace(input)
	parts := strings.Split(input, "#")
	if len(parts) != 2 {
		return issueRef{}, fmt.Errorf("invalid issue ref %q, expected OWNER/REPO#123", input)
	}
	repo := state.NormalizeRepo(parts[0])
	if repo == "" {
		return issueRef{}, fmt.Errorf("invalid repo in %q", input)
	}
	var issueNumber int
	if _, err := fmt.Sscanf(parts[1], "%d", &issueNumber); err != nil || issueNumber <= 0 {
		return issueRef{}, fmt.Errorf("invalid issue number in %q", input)
	}
	return issueRef{Repo: repo, IssueNumber: issueNumber}, nil
}

func normalizeIssueLikeTaskID(taskID string) string {
	ref, err := parseIssueRef(taskID)
	if err != nil {
		return strings.TrimSpace(taskID)
	}
	return fmt.Sprintf("%s#%d", ref.Repo, ref.IssueNumber)
}
