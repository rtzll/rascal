package runner

import (
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runtrigger"
)

func TestNormalizeSessionMode(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":         SessionModeOff,
		"off":      SessionModeOff,
		"pr-only":  SessionModePROnly,
		"PR-ONLY":  SessionModePROnly,
		"all":      SessionModeAll,
		"unknown":  SessionModeOff,
		"  all  ":  SessionModeAll,
		" pr-only": SessionModePROnly,
	}
	for in, want := range tests {
		if got := NormalizeSessionMode(in); got != want {
			t.Fatalf("NormalizeSessionMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSessionEnabled(t *testing.T) {
	t.Parallel()

	if SessionEnabled(SessionModeOff, runtrigger.NamePRComment) {
		t.Fatal("off mode should never enable sessions")
	}
	if !SessionEnabled(SessionModeAll, runtrigger.NameIssueLabel) {
		t.Fatal("all mode should enable sessions for all triggers")
	}
	for _, trigger := range []runtrigger.Name{
		runtrigger.NamePRComment,
		runtrigger.NamePRReview,
		runtrigger.NamePRReviewComment,
		runtrigger.NamePRReviewThread,
		runtrigger.NameRetry,
		runtrigger.NameIssueEdited,
	} {
		if !SessionEnabled(SessionModePROnly, trigger) {
			t.Fatalf("pr-only mode should enable trigger %q", trigger)
		}
	}
	if SessionEnabled(SessionModePROnly, runtrigger.NameIssueLabel) {
		t.Fatal("pr-only mode should not enable issue_label")
	}
}

func TestSessionTaskKeyStableAndBounded(t *testing.T) {
	t.Parallel()

	a := SessionTaskKey("Owner/Repo", "owner/repo#123")
	b := SessionTaskKey("Owner/Repo", "owner/repo#123")
	c := SessionTaskKey("Owner/Repo", "owner/repo#124")

	if a != b {
		t.Fatalf("expected stable key, got %q vs %q", a, b)
	}
	if a == c {
		t.Fatalf("expected different keys for different task IDs, got %q", a)
	}
	if len(a) > 56 {
		t.Fatalf("expected bounded key length <= 56, got %d", len(a))
	}
	for _, ch := range a {
		if (ch >= 'a' && ch <= 'z') || (ch >= '0' && ch <= '9') || ch == '-' {
			continue
		}
		t.Fatalf("unexpected key character %q in %q", ch, a)
	}
}

func TestSessionNameBounded(t *testing.T) {
	t.Parallel()

	name := SessionName(strings.Repeat("repo-", 20), strings.Repeat("task-", 20))
	if !strings.HasPrefix(name, "rascal-") {
		t.Fatalf("expected name prefix rascal-, got %q", name)
	}
	if len(name) > 63 {
		t.Fatalf("expected name length <= 63, got %d", len(name))
	}
}
