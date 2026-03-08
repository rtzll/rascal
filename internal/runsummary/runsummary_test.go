package runsummary

import (
	"strings"
	"testing"
	"time"
)

func TestParseCommitBody(t *testing.T) {
	t.Run("parses body after title", func(t *testing.T) {
		got, err := ParseCommitBody([]byte("feat(rascal): title\n\n- item 1\n- item 2\n"))
		if err != nil {
			t.Fatalf("ParseCommitBody returned error: %v", err)
		}
		want := "- item 1\n- item 2"
		if got != want {
			t.Fatalf("unexpected body: got %q want %q", got, want)
		}
	})

	t.Run("empty when only title", func(t *testing.T) {
		got, err := ParseCommitBody([]byte("feat(rascal): title\n"))
		if err != nil {
			t.Fatalf("ParseCommitBody returned error: %v", err)
		}
		if got != "" {
			t.Fatalf("expected empty body, got %q", got)
		}
	})

	t.Run("skips leading blank lines before title", func(t *testing.T) {
		got, err := ParseCommitBody([]byte("\n\nfeat(rascal): title\n\nline\n"))
		if err != nil {
			t.Fatalf("ParseCommitBody returned error: %v", err)
		}
		if got != "line" {
			t.Fatalf("unexpected body: %q", got)
		}
	})
}

func TestExtractTotalTokens(t *testing.T) {
	got, ok := ExtractTotalTokens(`{"usage":{"total_tokens":12}}
{"usage":{"total_tokens":34}}`)
	if !ok {
		t.Fatal("expected token extraction success")
	}
	if got != 34 {
		t.Fatalf("unexpected token count: got %d want 34", got)
	}

	if _, ok := ExtractTotalTokens(`{"event":"x"}`); ok {
		t.Fatal("expected token extraction failure without total_tokens")
	}
}

func TestExtractTokenUsage(t *testing.T) {
	t.Run("extracts codex-style token breakdown", func(t *testing.T) {
		usage, ok := ExtractTokenUsage(`{"type":"turn.completed","model":"gpt-5-codex","usage":{"input_tokens":120,"input_tokens_details":{"cached_tokens":40},"output_tokens":30,"output_tokens_details":{"reasoning_tokens":10},"total_tokens":150}}`)
		if !ok {
			t.Fatal("expected structured token usage")
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
		if !strings.Contains(usage.RawUsageJSON, `"input_tokens":120`) {
			t.Fatalf("expected raw usage json, got %q", usage.RawUsageJSON)
		}
	})

	t.Run("prefers final complete event totals", func(t *testing.T) {
		usage, ok := ExtractTokenUsage(`{"type":"message","usage":{"input_tokens":10,"output_tokens":5,"total_tokens":15}}
{"type":"complete","total_tokens":42}`)
		if !ok {
			t.Fatal("expected token usage from complete event")
		}
		if usage.TotalTokens != 42 {
			t.Fatalf("total_tokens = %d, want 42", usage.TotalTokens)
		}
		if usage.InputTokens != nil || usage.OutputTokens != nil {
			t.Fatalf("expected final complete event to avoid stale breakdown, got input=%v output=%v", usage.InputTokens, usage.OutputTokens)
		}
	})
}

func TestFormatDuration(t *testing.T) {
	cases := []struct {
		seconds int64
		want    string
	}{
		{0, "0s"},
		{59, "59s"},
		{61, "1m 1s"},
		{3600, "1h 0m 0s"},
		{3661, "1h 1m 1s"},
		{-1, "0s"},
	}
	for _, tc := range cases {
		got := FormatDuration(tc.seconds)
		if got != tc.want {
			t.Fatalf("FormatDuration(%d) = %q, want %q", tc.seconds, got, tc.want)
		}
	}
}

func TestFormatTokenCount(t *testing.T) {
	cases := []struct {
		tokens int64
		want   string
	}{
		{42, "42"},
		{20_000, "20K"},
		{100_999, "100K"},
		{1_000_000, "1.00M"},
		{1_010_000, "1.01M"},
		{5_470_139, "5.47M"},
	}
	for _, tc := range cases {
		got := formatTokenCount(tc.tokens)
		if got != tc.want {
			t.Fatalf("formatTokenCount(%d) = %q, want %q", tc.tokens, got, tc.want)
		}
	}
}

func TestBuildPRBody(t *testing.T) {
	t.Run("includes agent details and token summary when tokens are present", func(t *testing.T) {
		body := BuildPRBody(
			"run_1",
			"- updated code",
			`{"usage":{"total_tokens":123000}}`,
			"1m 2s",
			"",
		)
		if !strings.Contains(body, "<details><summary>Agent Details</summary>") {
			t.Fatalf("missing agent details section:\n%s", body)
		}
		if !strings.Contains(body, "Rascal run `run_1` completed in 1m 2s · 123K tokens") {
			t.Fatalf("missing token summary:\n%s", body)
		}
		if !strings.Contains(body, "- updated code") {
			t.Fatalf("missing commit body:\n%s", body)
		}
	})

	t.Run("falls back to run details without token summary", func(t *testing.T) {
		body := BuildPRBody(
			"run_2",
			"",
			`{"event":"x"}`,
			"8s",
			"\n\nCloses #12",
		)
		if !strings.Contains(body, "<details><summary>Agent Details</summary>") {
			t.Fatalf("missing agent details section:\n%s", body)
		}
		if strings.Contains(body, "consumed") {
			t.Fatalf("unexpected token summary:\n%s", body)
		}
		if !strings.Contains(body, "Automated changes from Rascal run run_2.") {
			t.Fatalf("missing default intro:\n%s", body)
		}
		if !strings.Contains(body, "Closes #12") {
			t.Fatalf("missing closes section:\n%s", body)
		}
	})

	t.Run("escapes nested fences and html-like content inside details", func(t *testing.T) {
		body := BuildPRBody(
			"run_3",
			"",
			"issue body\n```go\nfmt.Println(\"hi\")\n```\n<details>raw</details>\n{\"usage\":{\"total_tokens\":321}}",
			"9s",
			"",
		)
		if !strings.Contains(body, "<details><summary>Agent Details</summary>") {
			t.Fatalf("missing agent details section:\n%s", body)
		}
		if !strings.Contains(body, "<pre><code>") {
			t.Fatalf("expected html code wrapper:\n%s", body)
		}
		if !strings.Contains(body, "&lt;details&gt;raw&lt;/details&gt;") {
			t.Fatalf("expected html-like content to be escaped:\n%s", body)
		}
		if strings.Contains(body, "<summary>Agent Details</summary>\n\n```") {
			t.Fatalf("expected markdown fence wrapper to be removed:\n%s", body)
		}
		if strings.Count(body, "</details>") != 1 {
			t.Fatalf("expected a single outer details close tag:\n%s", body)
		}
	})
}

func TestBuildCompletionComment(t *testing.T) {
	t.Run("includes mention and commit link when requester and sha are present", func(t *testing.T) {
		body, err := BuildCompletionComment(CompletionCommentInput{
			RunID:           "run_1",
			Repo:            "owner/repo",
			RequestedBy:     "alice",
			HeadSHA:         "0123456789abcdef0123456789abcdef01234567",
			IssueNumber:     12,
			GooseOutput:     `{"usage":{"total_tokens":42000}}`,
			CommitMessage:   []byte("feat(rascal): update\n\n- item\n"),
			DurationSeconds: 65,
		})
		if err != nil {
			t.Fatalf("BuildCompletionComment returned error: %v", err)
		}
		if !strings.Contains(body, "@alice implemented in commit [`0123456789ab`]") {
			t.Fatalf("expected mention + commit link:\n%s", body)
		}
		if !strings.Contains(body, "Closes #12") {
			t.Fatalf("expected closes section:\n%s", body)
		}
		if !strings.Contains(body, "Rascal run `run_1` completed in 1m 5s · 42K tokens") {
			t.Fatalf("expected duration + tokens:\n%s", body)
		}
	})

	t.Run("falls back when requester is empty", func(t *testing.T) {
		body, err := BuildCompletionComment(CompletionCommentInput{
			RunID:           "run_2",
			GooseOutput:     `{"event":"x"}`,
			CommitMessage:   nil,
			DurationSeconds: 8,
		})
		if err != nil {
			t.Fatalf("BuildCompletionComment returned error: %v", err)
		}
		if strings.HasPrefix(body, "@") {
			t.Fatalf("did not expect mention prefix:\n%s", body)
		}
		if !strings.Contains(body, "Rascal run took 8s") {
			t.Fatalf("expected duration summary:\n%s", body)
		}
	})

	t.Run("uses explicit total tokens when provided", func(t *testing.T) {
		totalTokens := int64(42000)
		body, err := BuildCompletionComment(CompletionCommentInput{
			RunID:           "run_3",
			GooseOutput:     `{"event":"x"}`,
			CommitMessage:   []byte("feat(rascal): update"),
			DurationSeconds: 8,
			TotalTokens:     &totalTokens,
		})
		if err != nil {
			t.Fatalf("BuildCompletionComment returned error: %v", err)
		}
		if !strings.Contains(body, "Rascal run `run_3` completed in 8s · 42K tokens") {
			t.Fatalf("expected explicit token summary:\n%s", body)
		}
	})
}

func TestBuildStartComment(t *testing.T) {
	t.Run("keeps top line concise and moves settings into details", func(t *testing.T) {
		queueDelay := int64(18)
		body := BuildStartComment(StartCommentInput{
			RunID:             "run_abc123",
			RequestedBy:       "alice",
			Trigger:           "issue_label",
			Backend:           "codex",
			BaseBranch:        "main",
			HeadBranch:        "rascal/fix-flaky-test-abc123",
			SessionMode:       "all",
			SessionResume:     true,
			Debug:             true,
			Task:              "Fix flaky scheduler retry path",
			Context:           "Triggered by label 'rascal' on issue #123",
			QueueDelaySeconds: &queueDelay,
		})
		if !strings.HasPrefix(body, "Rascal started run `run_abc123` for this issue.") {
			t.Fatalf("expected concise top line, got:\n%s", body)
		}
		if !strings.Contains(body, "<details><summary>Run Settings</summary>") {
			t.Fatalf("expected run settings details block, got:\n%s", body)
		}
		if !strings.Contains(body, "- Requested by: `alice`") {
			t.Fatalf("expected requester in details, got:\n%s", body)
		}
		if !strings.Contains(body, "- Queue delay: `18s`") {
			t.Fatalf("expected queue delay in details, got:\n%s", body)
		}
		if strings.Count(body, "run_abc123") != 1 {
			t.Fatalf("expected run id only in top line, got:\n%s", body)
		}
	})

	t.Run("uses PR feedback wording and compacts long task context", func(t *testing.T) {
		body := BuildStartComment(StartCommentInput{
			RunID:         "run_pr_feedback",
			Trigger:       "pr_review_comment",
			Backend:       "codex",
			BaseBranch:    "main",
			HeadBranch:    "rascal/pr-77",
			SessionMode:   "pr-only",
			SessionResume: false,
			Debug:         true,
			Task:          "Address review feedback\n\nwith extra whitespace",
			Context:       strings.Repeat("context ", 80),
		})
		if !strings.HasPrefix(body, "Rascal started run `run_pr_feedback` to address new PR feedback.") {
			t.Fatalf("expected PR feedback headline, got:\n%s", body)
		}
		if strings.Contains(body, "\n- Task: Address review feedback\n\nwith extra whitespace") {
			t.Fatalf("expected task text to be compacted, got:\n%s", body)
		}
		if !strings.Contains(body, "- Resume: `false`") {
			t.Fatalf("expected resume setting, got:\n%s", body)
		}
		if !strings.Contains(body, "...") {
			t.Fatalf("expected long context to be truncated, got:\n%s", body)
		}
	})
}

func TestRunDurationSeconds(t *testing.T) {
	now := time.Now().UTC()
	created := now.Add(-10 * time.Minute)
	started := now.Add(-2 * time.Minute)
	completed := now.Add(-30 * time.Second)

	got := RunDurationSeconds(created, &started, &completed)
	if got != 90 {
		t.Fatalf("RunDurationSeconds = %d, want 90", got)
	}

	before := now.Add(-10 * time.Second)
	after := now.Add(-20 * time.Second)
	if got := RunDurationSeconds(before, nil, &after); got != 0 {
		t.Fatalf("RunDurationSeconds should clamp negatives to 0, got %d", got)
	}
}
