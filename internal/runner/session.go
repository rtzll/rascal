package runner

import (
	"strings"

	"github.com/rtzll/rascal/internal/agent"
)

const (
	GooseSessionModeOff    = string(agent.SessionModeOff)
	GooseSessionModePROnly = string(agent.SessionModePROnly)
	GooseSessionModeAll    = string(agent.SessionModeAll)
)

func NormalizeGooseSessionMode(mode string) string {
	return string(agent.NormalizeSessionMode(mode))
}

func GooseSessionEnabled(mode, trigger string) bool {
	return agent.SessionEnabled(agent.NormalizeSessionMode(mode), strings.TrimSpace(trigger))
}

func GooseSessionTaskKey(repo, taskID string) string {
	return agent.SessionTaskKey(repo, taskID)
}

func GooseSessionName(repo, taskID string) string {
	return "rascal-" + GooseSessionTaskKey(repo, taskID)
}
