package runtime

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/runtrigger"
)

type Harness string

const (
	HarnessGoose  Harness = "goose"
	HarnessDirect Harness = "direct"
)

type Runtime string

const (
	RuntimeGooseCodex  Runtime = "goose-codex"
	RuntimeCodex       Runtime = "codex"
	RuntimeClaude      Runtime = "claude"
	RuntimeGooseClaude Runtime = "goose-claude"
)

func NormalizeRuntime(raw string) Runtime {
	runtime, err := ParseRuntime(raw)
	if err != nil {
		return RuntimeGooseCodex
	}
	return runtime
}

func (r Runtime) String() string {
	return string(NormalizeRuntime(string(r)))
}

func ParseRuntime(raw string) (Runtime, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(RuntimeGooseCodex), "goose":
		return RuntimeGooseCodex, nil
	case string(RuntimeCodex):
		return RuntimeCodex, nil
	case string(RuntimeClaude):
		return RuntimeClaude, nil
	case string(RuntimeGooseClaude):
		return RuntimeGooseClaude, nil
	default:
		return "", fmt.Errorf("unknown agent runtime %q", raw)
	}
}

// Harness returns whether this runtime uses the goose harness or runs the
// provider directly.
func (r Runtime) Harness() Harness {
	switch NormalizeRuntime(string(r)) {
	case RuntimeGooseCodex, RuntimeGooseClaude:
		return HarnessGoose
	default:
		return HarnessDirect
	}
}

// Provider returns the model provider for this runtime.
func (r Runtime) Provider() ModelProvider {
	switch NormalizeRuntime(string(r)) {
	case RuntimeGooseCodex, RuntimeCodex:
		return ModelProviderCodex
	case RuntimeClaude, RuntimeGooseClaude:
		return ModelProviderAnthropic
	default:
		return ModelProviderCodex
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

func SessionEnabled(mode SessionMode, trigger runtrigger.Name) bool {
	switch NormalizeSessionMode(string(mode)) {
	case SessionModeAll:
		return true
	case SessionModePROnly:
		return trigger.EnablesPROnlySession()
	default:
		return false
	}
}

func TaskSessionKey(repo, taskID string) string {
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

func SessionTaskKey(repo, taskID string) string {
	return TaskSessionKey(repo, taskID)
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
