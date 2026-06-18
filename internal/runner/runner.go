package runner

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
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
	SecretsDir             string
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
	ModePodman Mode = "podman"
)

type PodmanSecurityMode string

const (
	PodmanSecurityOpen     PodmanSecurityMode = "open"
	PodmanSecurityBaseline PodmanSecurityMode = "baseline"
	PodmanSecurityStrict   PodmanSecurityMode = "strict"
)

type PodmanSecurityConfig struct {
	Mode            PodmanSecurityMode
	CPUs            string
	Memory          string
	PidsLimit       int
	TmpfsTmpSize    string
	AllowEnvSecrets bool
}

type ExecutionBackend string

const (
	ExecutionBackendPodman ExecutionBackend = "podman"
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
		Backend: ExecutionBackendPodman,
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
	case string(ModePodman):
		return ModePodman, nil
	default:
		return "", fmt.Errorf("unknown runner mode %q", raw)
	}
}

func NormalizePodmanSecurityMode(raw string) PodmanSecurityMode {
	mode, err := ParsePodmanSecurityMode(raw)
	if err != nil {
		return PodmanSecurityBaseline
	}
	return mode
}

func ParsePodmanSecurityMode(raw string) (PodmanSecurityMode, error) {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "", string(PodmanSecurityBaseline):
		return PodmanSecurityBaseline, nil
	case string(PodmanSecurityOpen):
		return PodmanSecurityOpen, nil
	case string(PodmanSecurityStrict):
		return PodmanSecurityStrict, nil
	default:
		return "", fmt.Errorf("unknown podman security mode %q", raw)
	}
}

func (c PodmanSecurityConfig) Normalize() PodmanSecurityConfig {
	c.Mode = NormalizePodmanSecurityMode(string(c.Mode))
	c.CPUs = strings.TrimSpace(c.CPUs)
	c.Memory = strings.TrimSpace(c.Memory)
	c.TmpfsTmpSize = strings.TrimSpace(c.TmpfsTmpSize)
	if c.PidsLimit < 0 {
		c.PidsLimit = 0
	}
	return c
}

func (c PodmanSecurityConfig) Summary() string {
	c = c.Normalize()
	parts := []string{fmt.Sprintf("mode=%s", c.Mode)}
	parts = append(parts, fmt.Sprintf("env_secrets=%t", c.AllowEnvSecrets))
	if c.Mode != PodmanSecurityOpen {
		parts = append(parts,
			fmt.Sprintf("cpus=%s", defaultSummaryValue(c.CPUs)),
			fmt.Sprintf("memory=%s", defaultSummaryValue(c.Memory)),
			fmt.Sprintf("pids=%s", defaultSummaryInt(c.PidsLimit)),
		)
	}
	if c.Mode == PodmanSecurityStrict {
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

func SecretsDir(runDir string) string {
	runDir = filepath.Clean(strings.TrimSpace(runDir))
	if runDir == "" {
		return ""
	}
	parent := filepath.Dir(runDir)
	base := filepath.Base(runDir)
	return filepath.Join(parent, "."+base+"-secrets")
}

// Runner starts a run inside an execution environment.
type Runner interface {
	StartDetached(ctx context.Context, spec Spec) (ExecutionHandle, error)
	Inspect(ctx context.Context, handle ExecutionHandle) (ExecutionState, error)
	Stop(ctx context.Context, handle ExecutionHandle, timeout time.Duration) error
	Remove(ctx context.Context, handle ExecutionHandle) error
}

func NewRunner(mode Mode, image, githubToken string, security PodmanSecurityConfig) Runner {
	switch NormalizeMode(string(mode)) {
	case ModePodman:
		return PodmanRunner{DefaultImage: image, GitHubToken: githubToken, Security: security.Normalize()}
	default:
		return NoopRunner{}
	}
}
