package runner

import (
	"context"
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
	RunDir      string
	IssueNumber int
	PRNumber    int
	Context     string
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

func NewLauncher(mode, image, githubToken string) Launcher {
	switch mode {
	case "docker":
		return DockerLauncher{Image: image, GitHubToken: githubToken}
	default:
		return NoopLauncher{}
	}
}
