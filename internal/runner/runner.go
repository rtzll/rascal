package runner

import (
	"context"
	"strings"
)

// RuntimeKind describes the runtime used to execute a run.
type RuntimeKind string

const (
	RuntimeDocker      RuntimeKind = "docker"
	RuntimeNoop        RuntimeKind = "noop"
	RuntimeFirecracker RuntimeKind = "firecracker"
)

// RuntimeKindFromString normalizes a runtime value into a supported kind.
func RuntimeKindFromString(value string) RuntimeKind {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case string(RuntimeDocker):
		return RuntimeDocker
	case string(RuntimeFirecracker):
		return RuntimeFirecracker
	default:
		return RuntimeNoop
	}
}

// LauncherConfig captures runtime selection and launcher wiring.
type LauncherConfig struct {
	Runtime     RuntimeKind
	ArtifactRef string
	GitHubToken string
}

// Spec defines the input contract for a single run.
type Spec struct {
	RunID          string
	TaskID         string
	Repo           string
	Task           string
	BaseBranch     string
	HeadBranch     string
	Trigger        string
	Debug          bool
	RunDir         string
	IssueNumber    int
	PRNumber       int
	Context        string
	CPUQuota       int
	CPUShares      int
	MemoryMB       int
	NetworkMode    string
	ReadonlyRoot   bool
	TimeoutSeconds int

	GooseSessionMode    string
	GooseSessionResume  bool
	GooseSessionTaskDir string
	GooseSessionTaskKey string
	GooseSessionName    string
}

// Result captures outputs emitted by the run environment.
type Result struct {
	PRNumber int
	PRURL    string
	HeadSHA  string
	ExitCode int
}

// Launcher starts a run inside an execution environment (Docker in v1).
type Launcher interface {
	Start(ctx context.Context, spec Spec) (Result, error)
}

func NewLauncher(cfg LauncherConfig) Launcher {
	switch cfg.Runtime {
	case RuntimeDocker:
		return DockerLauncher{Image: cfg.ArtifactRef, GitHubToken: cfg.GitHubToken}
	default:
		return NoopLauncher{}
	}
}
