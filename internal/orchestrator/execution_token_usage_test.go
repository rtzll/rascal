package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runsummary"
	agentrt "github.com/rtzll/rascal/internal/runtime"
	"github.com/rtzll/rascal/internal/state"
)

func int64Ptr(v int64) *int64 {
	return &v
}

func TestExecuteRunPersistsStructuredRunTokenUsage(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:          "run_token_usage",
		TaskID:      "owner/repo#88",
		Repo:        "owner/repo",
		Instruction: "Capture token usage",
		BaseBranch:  "main",
		HeadBranch:  "rascal/pr-88",
		Trigger:     "issue_label",
		RunDir:      t.TempDir(),
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	logBody := `{"type":"turn.completed","model":"gpt-5-codex","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":150}}`
	if err := os.WriteFile(filepath.Join(run.RunDir, "agent.ndjson"), []byte(logBody+"\n"), 0o644); err != nil {
		t.Fatalf("write agent log: %v", err)
	}

	s.ExecuteRun(run.ID)

	usage, ok := s.Store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected run token usage for %s", run.ID)
	}
	if usage.AgentRuntime != agentrt.RuntimeGooseCodex {
		t.Fatalf("backend = %q, want goose", usage.AgentRuntime)
	}
	if usage.Model != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", usage.Model)
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", usage.TotalTokens)
	}
	if usage.InputTokens == nil || *usage.InputTokens != 120 {
		t.Fatalf("input_tokens = %v, want 120", usage.InputTokens)
	}
	if usage.OutputTokens == nil || *usage.OutputTokens != 30 {
		t.Fatalf("output_tokens = %v, want 30", usage.OutputTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 40 {
		t.Fatalf("cached_input_tokens = %v, want 40", usage.CachedInputTokens)
	}
	if usage.ReasoningOutputTokens == nil || *usage.ReasoningOutputTokens != 10 {
		t.Fatalf("reasoning_output_tokens = %v, want 10", usage.ReasoningOutputTokens)
	}
	if !strings.Contains(usage.RawUsageJSON, `"reasoning_tokens":10`) {
		t.Fatalf("expected raw usage json, got %q", usage.RawUsageJSON)
	}
}

func TestExecuteRunPersistsRecordedCodexRunTokenUsage(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           "run_codex_token_usage",
		TaskID:       "owner/repo#99",
		Repo:         "owner/repo",
		Instruction:  "Capture codex token usage",
		BaseBranch:   "main",
		HeadBranch:   "rascal/pr-99",
		Trigger:      "issue_label",
		RunDir:       t.TempDir(),
		AgentRuntime: agentrt.RuntimeCodex,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := runsummary.WriteRecordedTokenUsage(filepath.Join(run.RunDir, runsummary.RecordedTokenUsageFile), runsummary.TokenUsage{
		Provider:              "openai",
		Model:                 "gpt-5-codex",
		TotalTokens:           150,
		InputTokens:           int64Ptr(120),
		OutputTokens:          int64Ptr(30),
		CachedInputTokens:     int64Ptr(40),
		ReasoningOutputTokens: int64Ptr(10),
		RawUsageJSON:          `{"total_tokens":150}`,
	}); err != nil {
		t.Fatalf("write recorded token usage: %v", err)
	}

	s.ExecuteRun(run.ID)

	usage, ok := s.Store.GetRunTokenUsage(run.ID)
	if !ok {
		t.Fatalf("expected run token usage for %s", run.ID)
	}
	if usage.AgentRuntime != agentrt.RuntimeCodex {
		t.Fatalf("backend = %q, want codex", usage.AgentRuntime)
	}
	if usage.Model != "gpt-5-codex" {
		t.Fatalf("model = %q, want gpt-5-codex", usage.Model)
	}
	if usage.TotalTokens != 150 {
		t.Fatalf("total_tokens = %d, want 150", usage.TotalTokens)
	}
	if usage.CachedInputTokens == nil || *usage.CachedInputTokens != 40 {
		t.Fatalf("cached_input_tokens = %v, want 40", usage.CachedInputTokens)
	}
}

func TestExecuteRunIgnoresInvalidRecordedCodexRunTokenUsage(t *testing.T) {
	t.Parallel()
	launcher := &executionFakeRunner{
		resSeq: []executionFakeRunResult{{
			HeadSHA: "0123456789abcdef0123456789abcdef01234567",
		}},
	}
	s := newExecutionTestServer(t, launcher)
	defer waitForExecutionServerIdle(t, s)

	run, err := s.Store.AddRun(state.CreateRunInput{
		ID:           "run_codex_token_usage_invalid",
		TaskID:       "owner/repo#100",
		Repo:         "owner/repo",
		Instruction:  "Capture invalid codex token usage",
		BaseBranch:   "main",
		HeadBranch:   "rascal/pr-100",
		Trigger:      "issue_label",
		RunDir:       t.TempDir(),
		AgentRuntime: agentrt.RuntimeCodex,
	})
	if err != nil {
		t.Fatalf("add run: %v", err)
	}
	if err := os.WriteFile(filepath.Join(run.RunDir, runsummary.RecordedTokenUsageFile), []byte("{"), 0o644); err != nil {
		t.Fatalf("write invalid recorded token usage: %v", err)
	}

	s.ExecuteRun(run.ID)

	if _, ok := s.Store.GetRunTokenUsage(run.ID); ok {
		t.Fatalf("did not expect persisted token usage for %s", run.ID)
	}
}
