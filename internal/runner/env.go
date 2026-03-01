package runner

import (
	"strconv"
	"strings"
)

const (
	envRunID                  = "RASCAL_RUN_ID"
	envTaskID                 = "RASCAL_TASK_ID"
	envTask                   = "RASCAL_TASK"
	envRepo                   = "RASCAL_REPO"
	envBaseBranch             = "RASCAL_BASE_BRANCH"
	envHeadBranch             = "RASCAL_HEAD_BRANCH"
	envTrigger                = "RASCAL_TRIGGER"
	envGooseDebug             = "RASCAL_GOOSE_DEBUG"
	envContext                = "RASCAL_CONTEXT"
	envContextJSON            = "RASCAL_CONTEXT_JSON"
	envIssueNumber            = "RASCAL_ISSUE_NUMBER"
	envPRNumber               = "RASCAL_PR_NUMBER"
	envCodexHome              = "CODEX_HOME"
	envGoosePathRoot          = "GOOSE_PATH_ROOT"
	envGooseProvider          = "GOOSE_PROVIDER"
	envGooseModel             = "GOOSE_MODEL"
	envGooseMode              = "GOOSE_MODE"
	envGooseDisableKeyring    = "GOOSE_DISABLE_KEYRING"
	envGooseDisableSessNaming = "GOOSE_DISABLE_SESSION_NAMING"
	envGooseContextStrategy   = "GOOSE_CONTEXT_STRATEGY"
	envGHPromptDisabled       = "GH_PROMPT_DISABLED"
	envGitTerminalPrompt      = "GIT_TERMINAL_PROMPT"
	envGHToken                = "GH_TOKEN"
)

// EnvBuilder assembles the container environment for a run.
type EnvBuilder struct {
	spec        Spec
	githubToken string
}

func NewEnvBuilder(spec Spec) *EnvBuilder {
	return &EnvBuilder{spec: spec}
}

func (b *EnvBuilder) WithGitHubToken(token string) *EnvBuilder {
	b.githubToken = token
	return b
}

func (b *EnvBuilder) Build() map[string]string {
	env := map[string]string{
		envRunID:                  b.spec.RunID,
		envTaskID:                 b.spec.TaskID,
		envTask:                   b.spec.Task,
		envRepo:                   b.spec.Repo,
		envBaseBranch:             b.spec.BaseBranch,
		envHeadBranch:             b.spec.HeadBranch,
		envTrigger:                b.spec.Trigger,
		envGooseDebug:             strconv.FormatBool(b.spec.Debug),
		envContext:                b.spec.Context,
		envContextJSON:            "/rascal-meta/context.json",
		envIssueNumber:            strconv.Itoa(b.spec.IssueNumber),
		envPRNumber:               strconv.Itoa(b.spec.PRNumber),
		envCodexHome:              "/rascal-meta/codex",
		envGoosePathRoot:          "/rascal-meta/goose",
		envGooseProvider:          "codex",
		envGooseModel:             "gpt-5.2-codex",
		envGooseMode:              "auto",
		envGooseDisableKeyring:    "1",
		envGooseDisableSessNaming: "true",
		envGooseContextStrategy:   "summarize",
		envGHPromptDisabled:       "1",
		envGitTerminalPrompt:      "0",
	}

	if strings.TrimSpace(b.githubToken) != "" {
		env[envGHToken] = b.githubToken
	}

	return env
}
