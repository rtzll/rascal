package runner

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

const (
	GooseSessionModeOff    = "off"
	GooseSessionModePROnly = "pr-only"
	GooseSessionModeAll    = "all"
)

func NormalizeGooseSessionMode(mode string) string {
	switch strings.ToLower(strings.TrimSpace(mode)) {
	case GooseSessionModePROnly:
		return GooseSessionModePROnly
	case GooseSessionModeAll:
		return GooseSessionModeAll
	default:
		return GooseSessionModeOff
	}
}

func GooseSessionEnabled(mode, trigger string) bool {
	switch NormalizeGooseSessionMode(mode) {
	case GooseSessionModeAll:
		return true
	case GooseSessionModePROnly:
		switch strings.TrimSpace(trigger) {
		case "pr_comment", "pr_review", "pr_review_comment", "retry", "issue_edited":
			return true
		default:
			return false
		}
	default:
		return false
	}
}

func GooseSessionTaskKey(repo, taskID string) string {
	const maxBaseLen = 45
	raw := strings.TrimSpace(repo) + "::" + strings.TrimSpace(taskID)
	sum := sha256.Sum256([]byte(raw))
	hash := hex.EncodeToString(sum[:])[:10]

	base := sanitizeSessionSlug(strings.ToLower(strings.TrimSpace(repo) + "-" + strings.TrimSpace(taskID)))
	if base == "" {
		base = "task"
	}
	if len(base) > maxBaseLen {
		base = strings.Trim(base[:maxBaseLen], "-")
		if base == "" {
			base = "task"
		}
	}
	return base + "-" + hash
}

func GooseSessionName(repo, taskID string) string {
	return "rascal-" + GooseSessionTaskKey(repo, taskID)
}

func sanitizeSessionSlug(in string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range in {
		switch {
		case r >= 'a' && r <= 'z':
			b.WriteRune(r)
			prevDash = false
		case r >= '0' && r <= '9':
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteByte('-')
				prevDash = true
			}
		}
	}
	return strings.Trim(b.String(), "-")
}
