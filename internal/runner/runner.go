package runner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/runtrigger"
)

// Spec defines the input contract for a single run.
type Spec struct {
	RunID                  string
	TaskID                 string
	Repo                   string
	Instruction            string
	AgentRuntime           runtime.Runtime
	RunnerImage            string
	BaseBranch             string
	HeadBranch             string
	Trigger                runtrigger.Name
	Debug                  bool
	RunDir                 string
	IssueNumber            int
	PRNumber               int
	Context                string
	ResultReportSocketPath string
	TaskSession            TaskSessionSpec
}

var ErrExecutionNotFound = errors.New("execution handle not found")

type Mode string

const (
	ModeNoop   Mode = "noop"
	ModeDocker Mode = "docker"
)

type DockerSecurityMode string

const (
	DockerSecurityOpen     DockerSecurityMode = "open"
	DockerSecurityBaseline DockerSecurityMode = "baseline"
	DockerSecurityStrict   DockerSecurityMode = "strict"
)

type DockerSecurityConfig struct {
	Mode         DockerSecurityMode
	CPUs         string
	Memory       string
	PidsLimit    int
	TmpfsTmpSize string
}

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

func NormalizeDockerSecurityMode(raw string) DockerSecurityMode {
	mode, err := ParseDockerSecurityMode(raw)
	if err != nil {
		return DockerSecurityBaseline
	}
	return mode
}

func ParseDockerSecurityMode(raw string) (DockerSecurityMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(DockerSecurityBaseline):
		return DockerSecurityBaseline, nil
	case string(DockerSecurityOpen):
		return DockerSecurityOpen, nil
	case string(DockerSecurityStrict):
		return DockerSecurityStrict, nil
	default:
		return "", fmt.Errorf("unknown docker security mode %q", raw)
	}
}

func (c DockerSecurityConfig) Normalize() DockerSecurityConfig {
	c.Mode = NormalizeDockerSecurityMode(string(c.Mode))
	c.CPUs = strings.TrimSpace(c.CPUs)
	c.Memory = strings.TrimSpace(c.Memory)
	c.TmpfsTmpSize = strings.TrimSpace(c.TmpfsTmpSize)
	if c.PidsLimit < 0 {
		c.PidsLimit = 0
	}
	return c
}

func (c DockerSecurityConfig) Summary() string {
	c = c.Normalize()
	parts := []string{fmt.Sprintf("mode=%s", c.Mode)}
	if c.Mode != DockerSecurityOpen {
		parts = append(parts,
			fmt.Sprintf("cpus=%s", defaultSummaryValue(c.CPUs)),
			fmt.Sprintf("memory=%s", defaultSummaryValue(c.Memory)),
			fmt.Sprintf("pids=%s", defaultSummaryInt(c.PidsLimit)),
		)
	}
	if c.Mode == DockerSecurityStrict {
		parts = append(parts, fmt.Sprintf("tmpfs_tmp=%s", defaultSummaryValue(c.TmpfsTmpSize)))
	}
	return strings.Join(parts, " ")
}

func defaultSummaryValue(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "off"
	}
	return value
}

func defaultSummaryInt(value int) string {
	if value <= 0 {
		return "off"
	}
	return strconv.Itoa(value)
}

// Runner starts a run inside an execution environment (Docker in v1).
type Runner interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewRunner(mode Mode, image, githubToken string, security DockerSecurityConfig) Runner {
	switch NormalizeMode(string(mode)) {
	case ModeDocker:
		return DockerRunner{DefaultImage: image, GitHubToken: githubToken, Security: security.Normalize()}
	default:
		return NoopRunner{}
	}
}
