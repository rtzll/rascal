package runsummary

import (
	"bufio"
	"bytes"
	"fmt"
	"html"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runtrigger"
)

const agentDetailsSummary = "Agent Details"

type CompletionCommentInput struct {
	RunID           string
	Repo            string
	RequestedBy     string
	HeadSHA         string
	IssueNumber     int
	GooseOutput     string
	CommitMessage   []byte
	DurationSeconds int64
	TotalTokens     *int64
}

type StartCommentInput struct {
	RunID             string
	RequestedBy       string
	Trigger           runtrigger.Name
	Backend           agent.Backend
	RunnerCommit      string
	BaseBranch        string
	HeadBranch        string
	SessionMode       string
	SessionResume     bool
	Debug             bool
	Task              string
	Context           string
	QueueDelaySeconds *int64
}

// ParseCommitBody extracts the optional commit body from commit_message.txt
// style content where the first non-empty line is the title.
func ParseCommitBody(data []byte) (string, error) {
	scanner := bufio.NewScanner(bytes.NewReader(data))
	sawTitle := false
	bodyLines := make([]string, 0)
	for scanner.Scan() {
		line := strings.TrimSuffix(scanner.Text(), "\r")
		if !sawTitle {
			if strings.TrimSpace(line) == "" {
				continue
			}
			sawTitle = true
			continue
		}
		bodyLines = append(bodyLines, line)
	}
	if err := scanner.Err(); err != nil {
		return "", fmt.Errorf("scan commit body: %w", err)
	}

	body := strings.Join(bodyLines, "\n")
	body = strings.TrimSuffix(body, "\n")
	for strings.HasPrefix(body, "\n") {
		body = strings.TrimPrefix(body, "\n")
	}
	return body, nil
}

func FormatDuration(totalSeconds int64) string {
	if totalSeconds < 0 {
		totalSeconds = 0
	}
	hours := totalSeconds / 3600
	minutes := (totalSeconds % 3600) / 60
	seconds := totalSeconds % 60
	parts := make([]string, 0, 3)
	if hours > 0 {
		parts = append(parts, fmt.Sprintf("%dh", hours))
	}
	if hours > 0 || minutes > 0 {
		parts = append(parts, fmt.Sprintf("%dm", minutes))
	}
	parts = append(parts, fmt.Sprintf("%ds", seconds))
	return strings.Join(parts, " ")
}

func formatTokenCount(totalTokens int64) string {
	if totalTokens < 0 {
		return "0"
	}
	if totalTokens >= 1_000_000 {
		return fmt.Sprintf("%.2fM", float64(totalTokens)/1_000_000.0)
	}
	if totalTokens >= 1_000 {
		return fmt.Sprintf("%dK", totalTokens/1_000)
	}
	return strconv.FormatInt(totalTokens, 10)
}

func renderAgentDetailsSection(gooseOutput string) string {
	return fmt.Sprintf(
		"<details><summary>%s</summary>\n\n<pre><code>%s</code></pre>\n\n</details>",
		agentDetailsSummary,
		html.EscapeString(gooseOutput),
	)
}

func BuildPRBody(runID, commitBody, gooseOutput, runDuration, closesSection string) string {
	gooseSection := renderAgentDetailsSection(gooseOutput)
	if usage, ok := ExtractTokenUsage(gooseOutput); ok {
		body := ""
		if strings.TrimSpace(commitBody) != "" {
			body = commitBody + "\n\n"
		}
		body += gooseSection + closesSection + "\n\n---\n\n" + fmt.Sprintf(
			"Rascal run `%s` completed in %s · %s tokens",
			runID,
			runDuration,
			formatTokenCount(usage.TotalTokens),
		)
		return body
	}

	body := fmt.Sprintf("Automated changes from Rascal run %s.", runID)
	if strings.TrimSpace(commitBody) != "" {
		body = commitBody + "\n\n" + body
	}
	body += "\n\n" + gooseSection + closesSection + "\n\n---\n\n" + fmt.Sprintf("Rascal run took %s", runDuration)
	return body
}

func BuildCompletionComment(in CompletionCommentInput) (string, error) {
	commitBody, err := ParseCommitBody(in.CommitMessage)
	if err != nil {
		return "", fmt.Errorf("parse commit body: %w", err)
	}
	closesSection := ""
	if in.IssueNumber > 0 {
		closesSection = fmt.Sprintf("\n\nCloses #%d", in.IssueNumber)
	}
	runDuration := FormatDuration(in.DurationSeconds)
	commentBody := BuildPRBody(in.RunID, commitBody, in.GooseOutput, runDuration, closesSection)
	if in.TotalTokens != nil && *in.TotalTokens > 0 {
		body := ""
		if strings.TrimSpace(commitBody) != "" {
			body = commitBody + "\n\n"
		}
		body += renderAgentDetailsSection(in.GooseOutput) + closesSection + "\n\n---\n\n" + fmt.Sprintf(
			"Rascal run `%s` completed in %s · %s tokens",
			in.RunID,
			runDuration,
			formatTokenCount(*in.TotalTokens),
		)
		commentBody = body
	}

	requestedBy := strings.TrimSpace(in.RequestedBy)
	if requestedBy == "" {
		return commentBody, nil
	}

	hasCommitMessage := strings.TrimSpace(string(in.CommitMessage)) != ""
	headSHA := strings.TrimSpace(in.HeadSHA)
	if headSHA == "" || !hasCommitMessage {
		return fmt.Sprintf("@%s posted the run details below.\n\n%s", requestedBy, commentBody), nil
	}

	shaShort := headSHA
	if len(shaShort) > 12 {
		shaShort = shaShort[:12]
	}
	repo := strings.TrimSpace(in.Repo)
	if repo == "" {
		return fmt.Sprintf("@%s implemented in commit `%s`.\n\n%s", requestedBy, shaShort, commentBody), nil
	}
	commitURL := fmt.Sprintf("https://github.com/%s/commit/%s", repo, headSHA)
	return fmt.Sprintf("@%s implemented in commit [`%s`](%s).\n\n%s", requestedBy, shaShort, commitURL, commentBody), nil
}

func BuildStartComment(in StartCommentInput) string {
	lines := []string{startCommentHeadline(in)}

	details := make([]string, 0, 10)
	if requestedBy := strings.TrimSpace(in.RequestedBy); requestedBy != "" {
		details = append(details, fmt.Sprintf("- Requested by: `%s`", requestedBy))
	}
	if trigger := strings.TrimSpace(in.Trigger.String()); trigger != "" {
		details = append(details, fmt.Sprintf("- Trigger: `%s`", trigger))
	}
	if backend := strings.TrimSpace(in.Backend.String()); backend != "" {
		details = append(details, fmt.Sprintf("- Backend: `%s`", backend))
	}
	if runnerCommit := strings.TrimSpace(in.RunnerCommit); runnerCommit != "" {
		details = append(details, fmt.Sprintf("- Runner commit: `%s`", runnerCommit))
	}
	if baseBranch := strings.TrimSpace(in.BaseBranch); baseBranch != "" || strings.TrimSpace(in.HeadBranch) != "" {
		details = append(details, fmt.Sprintf("- Branches: `%s` -> `%s`", defaultString(baseBranch, "(default)"), defaultString(strings.TrimSpace(in.HeadBranch), "(default)")))
	}
	if sessionMode := strings.TrimSpace(in.SessionMode); sessionMode != "" {
		details = append(details, fmt.Sprintf("- Session mode: `%s`", sessionMode))
		details = append(details, fmt.Sprintf("- Resume: `%t`", in.SessionResume))
	}
	details = append(details, fmt.Sprintf("- Debug: `%t`", in.Debug))
	if in.QueueDelaySeconds != nil {
		details = append(details, fmt.Sprintf("- Queue delay: `%s`", FormatDuration(*in.QueueDelaySeconds)))
	}
	if task := compactCommentText(in.Task, 280); task != "" {
		details = append(details, "- Task: "+task)
	}
	if contextText := compactCommentText(in.Context, 280); contextText != "" {
		details = append(details, "- Context: "+contextText)
	}
	if len(details) == 0 {
		return lines[0]
	}

	lines = append(lines, "<details><summary>Run Settings</summary>", "")
	lines = append(lines, details...)
	lines = append(lines, "", "</details>")
	return strings.Join(lines, "\n\n")
}

func RunDurationSeconds(created time.Time, started, completed *time.Time) int64 {
	start := created.UTC()
	if started != nil {
		start = started.UTC()
	}
	end := time.Now().UTC()
	if completed != nil {
		end = completed.UTC()
	}
	if end.Before(start) {
		return 0
	}
	return int64(end.Sub(start).Seconds())
}

func startCommentHeadline(in StartCommentInput) string {
	switch trigger := runtrigger.Normalize(in.Trigger.String()); {
	case trigger == runtrigger.NamePRComment || trigger == runtrigger.NamePRReview || trigger == runtrigger.NamePRReviewComment:
		return fmt.Sprintf("Rascal started run `%s` to address new PR feedback.", strings.TrimSpace(in.RunID))
	case trigger.IsIssue():
		return fmt.Sprintf("Rascal started run `%s` for this issue.", strings.TrimSpace(in.RunID))
	default:
		return fmt.Sprintf("Rascal started run `%s`.", strings.TrimSpace(in.RunID))
	}
}

func compactCommentText(value string, maxLen int) string {
	value = strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
	if maxLen <= 0 || len(value) <= maxLen {
		return value
	}
	if maxLen <= 3 {
		return value[:maxLen]
	}
	return strings.TrimSpace(value[:maxLen-3]) + "..."
}

func defaultString(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}
