package runner

import (
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
)

const (
	SessionModeOff    = string(runtime.SessionModeOff)
	SessionModePROnly = string(runtime.SessionModePROnly)
	SessionModeAll    = string(runtime.SessionModeAll)
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

func TaskSessionName(repo, taskID string) string {
	return "rascal-" + TaskSessionKey(repo, taskID)
}
