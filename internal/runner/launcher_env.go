package runner

import (
	"strconv"
	"strings"
)

func runnerEnv(spec Spec, githubToken string) map[string]string {
	envPairs := map[string]string{
		"RASCAL_RUN_ID":                spec.RunID,
		"RASCAL_TASK_ID":               spec.TaskID,
		"RASCAL_TASK":                  spec.Task,
		"RASCAL_REPO":                  spec.Repo,
		"RASCAL_BASE_BRANCH":           spec.BaseBranch,
		"RASCAL_HEAD_BRANCH":           spec.HeadBranch,
		"RASCAL_TRIGGER":               spec.Trigger,
		"RASCAL_GOOSE_DEBUG":           strconv.FormatBool(spec.Debug),
		"RASCAL_CONTEXT":               spec.Context,
		"RASCAL_CONTEXT_JSON":          "/rascal-meta/context.json",
		"RASCAL_ISSUE_NUMBER":          strconv.Itoa(spec.IssueNumber),
		"RASCAL_PR_NUMBER":             strconv.Itoa(spec.PRNumber),
		"RASCAL_GOOSE_SESSION_MODE":    NormalizeGooseSessionMode(spec.GooseSessionMode),
		"RASCAL_GOOSE_SESSION_RESUME":  strconv.FormatBool(spec.GooseSessionResume),
		"RASCAL_GOOSE_SESSION_KEY":     strings.TrimSpace(spec.GooseSessionTaskKey),
		"RASCAL_GOOSE_SESSION_NAME":    strings.TrimSpace(spec.GooseSessionName),
		"CODEX_HOME":                   "/rascal-meta/codex",
		"GOOSE_PATH_ROOT":              "/rascal-meta/goose",
		"GOOSE_PROVIDER":               "codex",
		"GOOSE_MODEL":                  "gpt-5.4",
		"GOOSE_MODE":                   "auto",
		"GOOSE_DISABLE_KEYRING":        "1",
		"GOOSE_DISABLE_SESSION_NAMING": "true",
		"GOOSE_CONTEXT_STRATEGY":       "summarize",
		"GH_PROMPT_DISABLED":           "1",
		"GIT_TERMINAL_PROMPT":          "0",
	}
	if strings.TrimSpace(githubToken) != "" {
		envPairs["GH_TOKEN"] = githubToken
	}
	return envPairs
}
