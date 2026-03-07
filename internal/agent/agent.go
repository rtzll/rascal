package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

type Backend string

const (
	BackendGoose Backend = "goose"
	BackendCodex Backend = "codex"
)

func NormalizeBackend(raw string) Backend {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(BackendCodex):
		return BackendCodex
	default:
		return BackendGoose
	}
}

func (b Backend) String() string {
	return string(NormalizeBackend(string(b)))
}

type SessionMode string

const (
	SessionModeOff    SessionMode = "off"
	SessionModePROnly SessionMode = "pr-only"
	SessionModeAll    SessionMode = "all"
)

func NormalizeSessionMode(raw string) SessionMode {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case string(SessionModePROnly):
		return SessionModePROnly
	case string(SessionModeAll):
		return SessionModeAll
	default:
		return SessionModeOff
	}
}

func SessionEnabled(mode SessionMode, trigger string) bool {
	switch NormalizeSessionMode(string(mode)) {
	case SessionModeAll:
		return true
	case SessionModePROnly:
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

func SessionTaskKey(repo, taskID string) string {
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
