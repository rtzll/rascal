package worker

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtrigger"
)

type Config struct {
	RunID       string
	TaskID      string
	Instruction string
	Repo        string
	BaseBranch  string
	HeadBranch  string
	IssueNumber int
	Trigger     runtrigger.Name
	GitHubToken string

	MetaDir          string
	WorkRoot         string
	RepoDir          string
	GooseLogPath     string
	MetaPath         string
	InstructionsPath string
	CommitMsgPath    string
	AgentOutputPath  string
	PRBodyPath       string

	GooseDebug   bool
	AgentRuntime agent.Runtime

	GoosePathRoot   string
	CodexHome       string
	ClaudeConfigDir string
	TaskSession     runner.TaskSessionSpec
}

func LoadConfig() (Config, error) {
	runID, err := requiredEnv("RASCAL_RUN_ID")
	if err != nil {
		return Config{}, err
	}
	taskID, err := requiredEnv("RASCAL_TASK_ID")
	if err != nil {
		return Config{}, err
	}
	repo, err := requiredEnv("RASCAL_REPO")
	if err != nil {
		return Config{}, err
	}
	ghToken, err := requiredEnv("GH_TOKEN")
	if err != nil {
		return Config{}, err
	}

	baseBranch := strings.TrimSpace(os.Getenv("RASCAL_BASE_BRANCH"))
	if baseBranch == "" {
		baseBranch = "main"
	}
	headBranch := strings.TrimSpace(os.Getenv("RASCAL_HEAD_BRANCH"))
	if headBranch == "" {
		headBranch = "rascal/" + runID
	}
	issueNumber := 0
	if raw := strings.TrimSpace(os.Getenv("RASCAL_ISSUE_NUMBER")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			return Config{}, fmt.Errorf("invalid RASCAL_ISSUE_NUMBER: %w", err)
		}
		issueNumber = n
	}

	trigger, err := runtrigger.ParseOrDefault(os.Getenv("RASCAL_TRIGGER"), runtrigger.NameCLI)
	if err != nil {
		return Config{}, fmt.Errorf("invalid RASCAL_TRIGGER: %w", err)
	}

	metaDir := firstNonEmptyValue(strings.TrimSpace(os.Getenv("RASCAL_META_DIR")), defaultMetaDir)
	workRoot := firstNonEmptyValue(strings.TrimSpace(os.Getenv("RASCAL_WORK_ROOT")), defaultWorkRoot)
	repoDir := strings.TrimSpace(os.Getenv("RASCAL_REPO_DIR"))
	if repoDir == "" {
		repoDir = filepath.Join(workRoot, defaultRepoDirName)
	}

	debug := true
	if raw := strings.TrimSpace(os.Getenv("RASCAL_GOOSE_DEBUG")); raw != "" {
		switch strings.ToLower(raw) {
		case "0", "false", "no", "off":
			debug = false
		default:
			debug = true
		}
	}

	agentRuntime, err := agent.ParseRuntime(strings.TrimSpace(os.Getenv("RASCAL_AGENT_RUNTIME")))
	if err != nil {
		return Config{}, fmt.Errorf("invalid RASCAL_AGENT_RUNTIME: %w", err)
	}
	agentSessionModeRaw := strings.TrimSpace(os.Getenv("RASCAL_TASK_SESSION_MODE"))
	agentSessionMode, err := agent.ParseSessionMode(agentSessionModeRaw)
	if err != nil {
		return Config{}, fmt.Errorf("invalid agent session mode: %w", err)
	}
	agentSessionResume := parseBoolEnv(strings.TrimSpace(os.Getenv("RASCAL_TASK_SESSION_RESUME")), false)
	if agentSessionMode == agent.SessionModeOff {
		agentSessionResume = false
	}
	agentSessionKey := strings.TrimSpace(os.Getenv("RASCAL_TASK_SESSION_KEY"))
	backendSessionID := strings.TrimSpace(os.Getenv("RASCAL_TASK_SESSION_ID"))
	if agentSessionResume {
		if agentSessionKey == "" {
			agentSessionKey = runner.TaskSessionKey(repo, taskID)
		}
		if backendSessionID == "" && agentRuntime.Harness() == agent.HarnessGoose {
			backendSessionID = runner.TaskSessionName(repo, taskID)
		}
	}
	goosePathRoot := firstNonEmptyValue(strings.TrimSpace(os.Getenv("GOOSE_PATH_ROOT")), filepath.Join(metaDir, "goose"))
	codexHome := firstNonEmptyValue(strings.TrimSpace(os.Getenv("CODEX_HOME")), filepath.Join(metaDir, "codex"))
	claudeConfigDir := firstNonEmptyValue(strings.TrimSpace(os.Getenv("CLAUDE_CONFIG_DIR")), filepath.Join(metaDir, "claude"))

	return Config{
		RunID:            runID,
		TaskID:           taskID,
		Instruction:      strings.TrimSpace(os.Getenv("RASCAL_INSTRUCTION")),
		Repo:             repo,
		BaseBranch:       baseBranch,
		HeadBranch:       headBranch,
		IssueNumber:      issueNumber,
		Trigger:          trigger,
		GitHubToken:      ghToken,
		MetaDir:          metaDir,
		WorkRoot:         workRoot,
		RepoDir:          repoDir,
		GooseLogPath:     filepath.Join(metaDir, defaultAgentLogFile),
		MetaPath:         filepath.Join(metaDir, defaultMetaFile),
		InstructionsPath: filepath.Join(metaDir, defaultInstructionsFile),
		CommitMsgPath:    filepath.Join(metaDir, defaultCommitMsgFile),
		AgentOutputPath:  filepath.Join(metaDir, defaultAgentOutputFile),
		PRBodyPath:       filepath.Join(metaDir, defaultPRBodyFile),
		GooseDebug:       debug,
		AgentRuntime:     agentRuntime,
		GoosePathRoot:    goosePathRoot,
		CodexHome:        codexHome,
		ClaudeConfigDir:  claudeConfigDir,
		TaskSession: runner.TaskSessionSpec{
			Mode:             agentSessionMode,
			Resume:           agentSessionResume,
			TaskKey:          agentSessionKey,
			RuntimeSessionID: backendSessionID,
		},
	}, nil
}

func firstNonEmptyValue(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func parseBoolEnv(raw string, fallback bool) bool {
	if strings.TrimSpace(raw) == "" {
		return fallback
	}
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func configuredAgentRuntime(cfg Config) agent.Runtime {
	return agent.NormalizeRuntime(string(cfg.AgentRuntime))
}

func configuredSessionMode(cfg Config) agent.SessionMode {
	return agent.NormalizeSessionMode(string(cfg.TaskSession.Mode))
}

func configuredSessionResume(cfg Config) bool {
	return cfg.TaskSession.Resume
}

func configuredSessionKey(cfg Config) string {
	return strings.TrimSpace(cfg.TaskSession.TaskKey)
}

func configuredRuntimeSessionID(cfg Config) string {
	return strings.TrimSpace(cfg.TaskSession.RuntimeSessionID)
}

func ensureInstructions(cfg Config) error {
	if _, err := os.Stat(cfg.InstructionsPath); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat instructions: %w", err)
	}
	body := fmt.Sprintf(`# Rascal Instructions

Task ID: %s
Trigger: %s

Follow the repository instructions and implement the requested task.
Keep changes minimal, run `+"`make lint`"+` and `+"`make test`"+` before finishing if those targets exist, and summarize what changed.
If one of those commands does not exist or cannot run, explain exactly why and run the closest equivalent checks instead.
`, cfg.TaskID, cfg.Trigger)
	if err := os.WriteFile(cfg.InstructionsPath, []byte(body), 0o644); err != nil {
		return fmt.Errorf("write default instructions: %w", err)
	}
	return nil
}

func requiredEnv(key string) (string, error) {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return "", fmt.Errorf("%s is required", key)
	}
	return v, nil
}

func requireCommands(ex CommandExecutor, names ...string) error {
	for _, name := range names {
		if err := ex.LookPath(name); err != nil {
			return fmt.Errorf("required command missing: %s", name)
		}
	}
	return nil
}

func validateCommands(ex CommandExecutor, cfg Config) error {
	names := []string{"git", "gh"}
	switch configuredAgentRuntime(cfg) {
	case agent.RuntimeCodex:
		names = append(names, "codex")
	case agent.RuntimeClaude:
		names = append(names, "claude")
	case agent.RuntimeGooseClaude:
		names = append(names, "goose", "claude")
	default:
		names = append(names, "goose")
	}
	return requireCommands(ex, names...)
}
