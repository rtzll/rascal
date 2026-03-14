package agent

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

type Backend string

const (
	BackendGoose Backend = "goose"
	BackendCodex Backend = "codex"
)

func NormalizeBackend(raw string) Backend {
	backend, err := ParseBackend(raw)
	if err != nil {
		return BackendCodex
	}
	return backend
}

func (b Backend) String() string {
	return string(NormalizeBackend(string(b)))
}

func ParseBackend(raw string) (Backend, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(BackendCodex):
		return BackendCodex, nil
	case string(BackendGoose):
		return BackendGoose, nil
	default:
		return "", fmt.Errorf("unknown agent backend %q", raw)
	}
}

type SessionMode string

const (
	SessionModeOff    SessionMode = "off"
	SessionModePROnly SessionMode = "pr-only"
	SessionModeAll    SessionMode = "all"
)

func NormalizeSessionMode(raw string) SessionMode {
	mode, err := ParseSessionMode(raw)
	if err != nil {
		return SessionModeOff
	}
	return mode
}

func ParseSessionMode(raw string) (SessionMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(SessionModeOff):
		return SessionModeOff, nil
	case string(SessionModePROnly):
		return SessionModePROnly, nil
	case string(SessionModeAll):
		return SessionModeAll, nil
	default:
		return "", fmt.Errorf("unknown agent session mode %q", raw)
	}
}

func SessionEnabled(mode SessionMode, trigger string) bool {
	switch NormalizeSessionMode(string(mode)) {
	case SessionModeAll:
		return true
	case SessionModePROnly:
		switch strings.TrimSpace(trigger) {
		case "pr_comment", "pr_review", "pr_review_comment", "pr_review_thread", "retry", "issue_edited":
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
