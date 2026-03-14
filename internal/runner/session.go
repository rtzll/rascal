package runner

import (
	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runtrigger"
)

const (
	SessionModeOff    = string(agent.SessionModeOff)
	SessionModePROnly = string(agent.SessionModePROnly)
	SessionModeAll    = string(agent.SessionModeAll)

	GooseSessionModeOff    = SessionModeOff
	GooseSessionModePROnly = SessionModePROnly
	GooseSessionModeAll    = SessionModeAll
)

func NormalizeSessionMode(mode string) string {
	return string(agent.NormalizeSessionMode(mode))
}

func SessionEnabled(mode string, trigger runtrigger.Name) bool {
	return agent.SessionEnabled(agent.NormalizeSessionMode(mode), trigger)
}

func SessionTaskKey(repo, taskID string) string {
	return agent.SessionTaskKey(repo, taskID)
}

func SessionName(repo, taskID string) string {
	return "rascal-" + SessionTaskKey(repo, taskID)
}

func NormalizeGooseSessionMode(mode string) string {
	return NormalizeSessionMode(mode)
}

func GooseSessionEnabled(mode string, trigger runtrigger.Name) bool {
	return SessionEnabled(mode, trigger)
}

func GooseSessionTaskKey(repo, taskID string) string {
	return SessionTaskKey(repo, taskID)
}

func GooseSessionName(repo, taskID string) string {
	return SessionName(repo, taskID)
}
