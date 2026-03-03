package runsummary

import (
	"strings"
	"testing"
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
