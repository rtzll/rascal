package runner

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
)

// Spec defines the input contract for a single run.
type Spec struct {
	RunID        string
	TaskID       string
	Repo         string
	Instruction  string
	AgentRuntime runtime.Runtime
	RunnerImage  string
	BaseBranch   string
	HeadBranch   string
	Trigger      runtrigger.Name
	Debug        bool
	RunDir       string
	IssueNumber  int
	PRNumber     int
	Context      string
	TaskSession  TaskSessionSpec
}

var ErrExecutionNotFound = errors.New("execution handle not found")

type Mode string

const (
	ModeNoop   Mode = "noop"
	ModeDocker Mode = "docker"
)

type ExecutionBackend string

const (
	ExecutionBackendDocker ExecutionBackend = "docker"
	ExecutionBackendNoop   ExecutionBackend = "noop"
)

type ExecutionHandle struct {
	Backend ExecutionBackend
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
		Backend: ExecutionBackendDocker,
		Name:    name,
	}
}

type TaskSessionSpec struct {
	Mode             runtime.SessionMode
	Resume           bool
	TaskDir          string
	TaskKey          string
	RuntimeSessionID string
}

type SessionSpec = TaskSessionSpec

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

// Runner starts a run inside an execution environment (Docker in v1).
type Runner interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewRunner(mode Mode, image, githubToken string) Runner {
	switch NormalizeMode(string(mode)) {
	case ModeDocker:
		return DockerRunner{DefaultImage: image, GitHubToken: githubToken}
	default:
		return NoopRunner{}
	}
}
