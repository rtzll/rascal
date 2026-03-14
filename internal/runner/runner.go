package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
)

// Spec defines the input contract for a single run.
type Spec struct {
	RunID        string
	TaskID       string
	Repo         string
	Task         string
	AgentBackend agent.Backend
	RunnerImage  string
	BaseBranch   string
	HeadBranch   string
	Trigger      string
	Debug        bool
	RunDir       string
	IssueNumber  int
	PRNumber     int
	Context      string
	AgentSession SessionSpec
}

var ErrExecutionNotFound = errors.New("execution handle not found")

type Mode string

const (
	ModeNoop   Mode = "noop"
	ModeDocker Mode = "docker"
)

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

type SessionSpec struct {
	Mode             agent.SessionMode
	Resume           bool
	TaskDir          string
	TaskKey          string
	BackendSessionID string
}

func NormalizeMode(raw string) Mode {
	mode, err := ParseMode(raw)
	if err != nil {
		return ModeNoop
	}
	return mode
}

func ParseMode(raw string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(ModeNoop):
		return ModeNoop, nil
	case string(ModeDocker):
		return ModeDocker, nil
	default:
		return "", fmt.Errorf("unknown runner mode %q", raw)
	}
}

// Launcher starts a run inside an execution environment (Docker in v1).
type Launcher interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewLauncher(mode Mode, image, githubToken string) Launcher {
	switch NormalizeMode(string(mode)) {
	case ModeDocker:
		return DockerLauncher{DefaultImage: image, GitHubToken: githubToken}
	default:
		return NoopLauncher{}
	}
}
