package agent

import "github.com/rtzll/rascal/internal/runtrigger"

// SessionPolicy controls whether a task-scoped session may resume across runs.
type SessionPolicy = SessionMode

const (
	SessionPolicyOff    = SessionModeOff
	SessionPolicyPROnly = SessionModePROnly
	SessionPolicyAll    = SessionModeAll
)

func ParseSessionPolicy(raw string) (SessionPolicy, error) {
	return ParseSessionMode(raw)
}

func NormalizeSessionPolicy(raw string) SessionPolicy {
	return NormalizeSessionMode(raw)
}

func SessionPolicyEnabled(policy SessionPolicy, trigger runtrigger.Name) bool {
	return SessionEnabled(policy, trigger)
}
