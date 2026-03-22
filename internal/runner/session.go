package runner

import (
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
)

const (
	SessionModeOff    = string(runtime.SessionModeOff)
	SessionModePROnly = string(runtime.SessionModePROnly)
	SessionModeAll    = string(runtime.SessionModeAll)

	GooseSessionModeOff    = SessionModeOff
	GooseSessionModePROnly = SessionModePROnly
	GooseSessionModeAll    = SessionModeAll
)

func NormalizeSessionMode(mode string) string {
	return string(runtime.NormalizeSessionMode(mode))
}

func SessionEnabled(mode string, trigger runtrigger.Name) bool {
	return runtime.SessionEnabled(runtime.NormalizeSessionMode(mode), trigger)
}

func TaskSessionKey(repo, taskID string) string {
	return runtime.TaskSessionKey(repo, taskID)
}

func SessionTaskKey(repo, taskID string) string {
	return TaskSessionKey(repo, taskID)
}

func TaskSessionName(repo, taskID string) string {
	return "rascal-" + TaskSessionKey(repo, taskID)
}

func SessionName(repo, taskID string) string {
	return TaskSessionName(repo, taskID)
}

func NormalizeGooseSessionMode(mode string) string {
	return NormalizeSessionMode(mode)
}

func GooseSessionEnabled(mode string, trigger runtrigger.Name) bool {
	return SessionEnabled(mode, trigger)
}

func GooseSessionTaskKey(repo, taskID string) string {
	return TaskSessionKey(repo, taskID)
}

func GooseSessionName(repo, taskID string) string {
	return TaskSessionName(repo, taskID)
}
