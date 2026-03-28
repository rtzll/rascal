package reviewhandoff

import (
	"strings"
	"testing"
)

func TestAnalyzePrefersCODEOWNERSAndHistorySignals(t *testing.T) {
	report := Analyze(Input{
		BaseRef: "main",
		HeadRef: "HEAD",
		ChangedFiles: []ChangedFile{
			{Path: "internal/worker/worker.go"},
			{Path: "internal/worker/worker_test.go"},
		},
		Codeowners: "*.go @lang-team\n/internal/worker/ @octo/reviewers\n",
		History: []HistoryTouch{
			{Path: "internal/worker/worker.go", Reviewer: "12345+alice@users.noreply.github.com"},
			{Path: "internal/worker/worker_test.go", Reviewer: "alice@users.noreply.github.com"},
		},
	})

	if len(report.SuggestedReviewers) < 2 {
		t.Fatalf("expected reviewer suggestions, got %#v", report.SuggestedReviewers)
	}
	if report.SuggestedReviewers[0].Reviewer != "@octo/reviewers" {
		t.Fatalf("first reviewer = %q, want CODEOWNERS match", report.SuggestedReviewers[0].Reviewer)
	}
	if !strings.Contains(strings.Join(report.SuggestedReviewers[0].Reasons, " "), "CODEOWNERS") {
		t.Fatalf("expected CODEOWNERS explanation, got %#v", report.SuggestedReviewers[0].Reasons)
	}
	foundHistory := false
	for _, reviewer := range report.SuggestedReviewers {
		if reviewer.Reviewer == "@alice" {
			foundHistory = true
		}
	}
	if !foundHistory {
		t.Fatalf("expected git history suggestion, got %#v", report.SuggestedReviewers)
	}
}

func TestAnalyzeNoReviewerFallback(t *testing.T) {
	report := Analyze(Input{
		BaseRef: "main",
		HeadRef: "HEAD",
		ChangedFiles: []ChangedFile{
			{Path: "docs/architecture.md"},
		},
	})

	if len(report.SuggestedReviewers) != 0 {
		t.Fatalf("expected no reviewers, got %#v", report.SuggestedReviewers)
	}
	if !strings.Contains(report.ReviewerSummary, "No high-confidence reviewer suggestion") {
		t.Fatalf("unexpected reviewer summary: %q", report.ReviewerSummary)
	}
}

func TestAnalyzeClassifiesHighRiskWhenProdChangesSkipTests(t *testing.T) {
	report := Analyze(Input{
		BaseRef: "main",
		HeadRef: "HEAD",
		ChangedFiles: []ChangedFile{
			{Path: "cmd/rascald/main.go"},
			{Path: "internal/credentials/broker.go"},
			{Path: ".github/workflows/ci.yml"},
			{Path: "deploy/systemd/rascal@.service"},
			{Path: "internal/worker/worker.go"},
		},
	})

	if report.Risk.Level != "high" {
		t.Fatalf("risk level = %q, want high (report=%#v)", report.Risk.Level, report)
	}
	if !strings.Contains(strings.Join(report.Risk.Reasons, " "), "without test updates") {
		t.Fatalf("expected missing test reason, got %#v", report.Risk.Reasons)
	}
	if !strings.Contains(strings.Join(report.NotableSignals, " "), "Config/deploy/runtime") {
		t.Fatalf("expected config/runtime signal, got %#v", report.NotableSignals)
	}
}

func TestUpsertPRSectionIsIdempotent(t *testing.T) {
	report := Analyze(Input{
		BaseRef: "main",
		HeadRef: "HEAD",
		ChangedFiles: []ChangedFile{
			{Path: "internal/worker/worker.go"},
			{Path: "internal/worker/worker_test.go"},
		},
	})

	original := "Existing body\n\n---\n\nFooter"
	once := UpsertPRSection(original, report)
	twice := UpsertPRSection(once, report)
	if once != twice {
		t.Fatalf("expected stable PR section update\nonce:\n%s\n\ntwice:\n%s", once, twice)
	}
	if strings.Count(once, PRSectionStartMarker) != 1 {
		t.Fatalf("expected exactly one managed section, got:\n%s", once)
	}
	if !strings.Contains(once, "\n\n---\n\nFooter") {
		t.Fatalf("expected footer to be preserved, got:\n%s", once)
	}
}
