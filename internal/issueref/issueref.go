package issueref

import (
	"fmt"
	"strconv"
	"strings"
)

type Ref struct {
	Repo   string
	Number int
}

func (r Ref) String() string {
	return fmt.Sprintf("%s#%d", r.Repo, r.Number)
}

func Parse(input string) (Ref, error) {
	raw := strings.TrimSpace(input)
	if strings.Count(raw, "#") != 1 {
		return Ref{}, invalidIssueRef(raw, "expected OWNER/REPO#123")
	}
	parts := strings.SplitN(raw, "#", 2)
	repo := normalizeRepo(parts[0])
	if !isValidRepo(repo) {
		return Ref{}, invalidIssueRef(raw, "repo must be OWNER/REPO")
	}
	number, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || number <= 0 {
		return Ref{}, invalidIssueRef(raw, "issue number must be a positive integer")
	}
	return Ref{Repo: repo, Number: number}, nil
}

func Normalize(repo string, number int) (Ref, error) {
	repo = normalizeRepo(repo)
	if !isValidRepo(repo) {
		return Ref{}, invalidIssueRef(fmt.Sprintf("%s#%d", strings.TrimSpace(repo), number), "repo must be OWNER/REPO")
	}
	if number <= 0 {
		return Ref{}, invalidIssueRef(fmt.Sprintf("%s#%d", repo, number), "issue number must be a positive integer")
	}
	return Ref{Repo: repo, Number: number}, nil
}

func normalizeRepo(repo string) string {
	return strings.ToLower(strings.TrimSpace(repo))
}

func isValidRepo(repo string) bool {
	if strings.Count(repo, "/") != 1 {
		return false
	}
	parts := strings.SplitN(repo, "/", 2)
	for _, part := range parts {
		if strings.TrimSpace(part) == "" {
			return false
		}
		if strings.ContainsAny(part, " \t\r\n") {
			return false
		}
	}
	return true
}

func invalidIssueRef(input, reason string) error {
	return fmt.Errorf("invalid issue ref %q: %s", input, reason)
}
