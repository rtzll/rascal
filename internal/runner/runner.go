package runner

import (
	"context"
	"errors"
	"strings"
	"time"
)

// Spec defines the input contract for a single run.
type Spec struct {
	RunID       string
	TaskID      string
	Repo        string
	Task        string
	BaseBranch  string
	HeadBranch  string
	Trigger     string
	Debug       bool
	RunDir      string
	IssueNumber int
	PRNumber    int
	Context     string

	GooseSessionMode    string
	GooseSessionResume  bool
	GooseSessionTaskDir string
	GooseSessionTaskKey string
	GooseSessionName    string
}

var ErrExecutionNotFound = errors.New("execution handle not found")

type ExecutionHandle struct {
	Backend string
	ID      string
	Name    string
}

type ExecutionState struct {
	Running  bool
	ExitCode *int
}

func ExecutionHandleForRun(runID string) ExecutionHandle {
	runID = strings.TrimSpace(runID)
	name := sanitizeContainerName("rascal-" + runID)
	return ExecutionHandle{
		Backend: "docker",
		Name:    name,
	}
}

// Launcher starts a run inside an execution environment (Docker in v1).
type Launcher interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewLauncher(mode, image, githubToken string) Launcher {
	switch mode {
	case "docker":
		return DockerLauncher{Image: image, GitHubToken: githubToken}
	default:
		return NoopLauncher{}
	}
}
