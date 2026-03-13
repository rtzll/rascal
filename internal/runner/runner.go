package runner

import (
	"context"
	"errors"
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
	MemoryLimit  string
	MemorySwap   string
	BaseBranch   string
	HeadBranch   string
	Trigger      string
	Debug        bool
	RunDir       string
	IssueNumber  int
	PRNumber     int
	Context      string
	AgentSession SessionSpec

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
	Running   bool
	ExitCode  *int
	OOMKilled bool
	Error     string
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

// Launcher starts a run inside an execution environment (Docker in v1).
type Launcher interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewLauncher(mode, image, githubToken, memoryLimit, memorySwap string) Launcher {
	switch mode {
	case "docker":
		return DockerLauncher{
			DefaultImage:      image,
			GitHubToken:       githubToken,
			DefaultMemory:     memoryLimit,
			DefaultMemorySwap: memorySwap,
		}
	default:
		return NoopLauncher{}
	}
}
