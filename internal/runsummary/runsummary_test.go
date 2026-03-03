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

func TestBuildPRBody(t *testing.T) {
	t.Run("includes goose details and token summary when tokens are present", func(t *testing.T) {
		body := BuildPRBody(
			"run_1",
			"- updated code",
			`{"usage":{"total_tokens":123}}`,
			"1m 2s",
			"",
		)
		if !strings.Contains(body, "<details><summary>Goose Details</summary>") {
			t.Fatalf("missing goose details section:\n%s", body)
		}
		if !strings.Contains(body, "Rascal run `run_1` took 1m 2s [consumed 123 tokens]") {
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
		if !strings.Contains(body, "<details><summary>Run Details</summary>") {
			t.Fatalf("missing run details section:\n%s", body)
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
}

func TestBuildCompletionComment(t *testing.T) {
	t.Run("includes mention and commit link when requester and sha are present", func(t *testing.T) {
		body, err := BuildCompletionComment(CompletionCommentInput{
			RunID:           "run_1",
			Repo:            "owner/repo",
			RequestedBy:     "alice",
			HeadSHA:         "0123456789abcdef0123456789abcdef01234567",
			IssueNumber:     12,
			GooseOutput:     `{"usage":{"total_tokens":42}}`,
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
		if !strings.Contains(body, "Rascal run `run_1` took 1m 5s [consumed 42 tokens]") {
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
