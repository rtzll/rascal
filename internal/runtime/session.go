package runtime

import "github.com/rtzll/rascal/internal/runtrigger"

// TaskSessionPolicy controls whether a task-scoped session may resume across runs.
type TaskSessionPolicy = SessionMode

const (
	TaskSessionPolicyOff    = SessionModeOff
	TaskSessionPolicyPROnly = SessionModePROnly
	TaskSessionPolicyAll    = SessionModeAll
)

type SessionPolicy = TaskSessionPolicy

const (
	SessionPolicyOff    = TaskSessionPolicyOff
	SessionPolicyPROnly = TaskSessionPolicyPROnly
	SessionPolicyAll    = TaskSessionPolicyAll
)

func ParseTaskSessionPolicy(raw string) (TaskSessionPolicy, error) {
	return ParseSessionMode(raw)
}

func ParseSessionPolicy(raw string) (SessionPolicy, error) {
	return ParseTaskSessionPolicy(raw)
}

func NormalizeTaskSessionPolicy(raw string) TaskSessionPolicy {
	return NormalizeSessionMode(raw)
}

func NormalizeSessionPolicy(raw string) SessionPolicy {
	return NormalizeTaskSessionPolicy(raw)
}

func TaskSessionPolicyEnabled(policy TaskSessionPolicy, trigger runtrigger.Name) bool {
	return SessionEnabled(policy, trigger)
}

func SessionPolicyEnabled(policy SessionPolicy, trigger runtrigger.Name) bool {
	return TaskSessionPolicyEnabled(policy, trigger)
}
