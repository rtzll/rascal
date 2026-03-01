package runner

import "testing"

func TestEnvBuilderBuild(t *testing.T) {
	t.Parallel()

	spec := Spec{
		RunID:       "run-123",
		TaskID:      "task-456",
		Repo:        "org/repo",
		Task:        "do-the-thing",
		BaseBranch:  "main",
		HeadBranch:  "feature",
		Trigger:     "manual",
		Debug:       true,
		IssueNumber: 42,
		PRNumber:    7,
		Context:     "context",
	}

	env := NewEnvBuilder(spec).WithGitHubToken("token").Build()

	checks := map[string]string{
		envRunID:                  "run-123",
		envTaskID:                 "task-456",
		envTask:                   "do-the-thing",
		envRepo:                   "org/repo",
		envBaseBranch:             "main",
		envHeadBranch:             "feature",
		envTrigger:                "manual",
		envGooseDebug:             "true",
		envContext:                "context",
		envContextJSON:            "/rascal-meta/context.json",
		envIssueNumber:            "42",
		envPRNumber:               "7",
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
		envGHToken:                "token",
	}

	for key, expected := range checks {
		got, ok := env[key]
		if !ok {
			t.Fatalf("missing env key %q", key)
		}
		if got != expected {
			t.Fatalf("env %q = %q, want %q", key, got, expected)
		}
	}
}

func TestEnvBuilderBuildSkipsEmptyGitHubToken(t *testing.T) {
	t.Parallel()

	spec := Spec{RunID: "run-1"}
	env := NewEnvBuilder(spec).WithGitHubToken("  \t\n").Build()

	if _, ok := env[envGHToken]; ok {
		t.Fatalf("expected GH_TOKEN to be omitted")
	}
}
