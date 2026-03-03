package runsummary

import (
	"bufio"
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
)

var totalTokensPattern = regexp.MustCompile(`"total_tokens"[[:space:]]*:[[:space:]]*([0-9]+)`)

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

// ExtractTotalTokens returns the last total_tokens value found in goose output.
func ExtractTotalTokens(gooseOutput string) (int64, bool) {
	matches := totalTokensPattern.FindAllStringSubmatch(gooseOutput, -1)
	if len(matches) == 0 {
		return 0, false
	}
	last := matches[len(matches)-1]
	if len(last) < 2 {
		return 0, false
	}
	n, err := strconv.ParseInt(last[1], 10, 64)
	if err != nil || n < 0 {
		return 0, false
	}
	return n, true
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

func BuildPRBody(runID, commitBody, gooseOutput, runDuration, closesSection string) string {
	gooseSection := "<details><summary>Run Details</summary>\n\n```\n" + gooseOutput + "\n```\n\n</details>"
	if totalTokens, ok := ExtractTotalTokens(gooseOutput); ok {
		gooseSection = "<details><summary>Goose Details</summary>\n\n```\n" + gooseOutput + "\n```\n\n</details>"
		body := ""
		if strings.TrimSpace(commitBody) != "" {
			body = commitBody + "\n\n"
		}
		body += gooseSection + closesSection + "\n\n---\n\n" + fmt.Sprintf("Rascal run `%s` took %s [consumed %d tokens]", runID, runDuration, totalTokens)
		return body
	}

	body := fmt.Sprintf("Automated changes from Rascal run %s.", runID)
	if strings.TrimSpace(commitBody) != "" {
		body = commitBody + "\n\n" + body
	}
	body += "\n\n" + gooseSection + closesSection + "\n\n---\n\n" + fmt.Sprintf("Rascal run took %s", runDuration)
	return body
}
