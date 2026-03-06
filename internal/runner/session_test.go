package runner

import (
	"strings"
	"testing"
)

func TestNormalizeGooseSessionMode(t *testing.T) {
	t.Parallel()

	tests := map[string]string{
		"":         GooseSessionModeOff,
		"off":      GooseSessionModeOff,
		"pr-only":  GooseSessionModePROnly,
		"PR-ONLY":  GooseSessionModePROnly,
		"all":      GooseSessionModeAll,
		"unknown":  GooseSessionModeOff,
		"  all  ":  GooseSessionModeAll,
		" pr-only": GooseSessionModePROnly,
	}
	for in, want := range tests {
		if got := NormalizeGooseSessionMode(in); got != want {
			t.Fatalf("NormalizeGooseSessionMode(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestGooseSessionEnabled(t *testing.T) {
	t.Parallel()

	if GooseSessionEnabled(GooseSessionModeOff, "pr_comment") {
		t.Fatal("off mode should never enable sessions")
	}
	if !GooseSessionEnabled(GooseSessionModeAll, "issue_label") {
		t.Fatal("all mode should enable sessions for all triggers")
	}
	for _, trigger := range []string{"pr_comment", "pr_review", "pr_review_comment", "retry", "issue_edited"} {
		if !GooseSessionEnabled(GooseSessionModePROnly, trigger) {
			t.Fatalf("pr-only mode should enable trigger %q", trigger)
		}
	}
	if GooseSessionEnabled(GooseSessionModePROnly, "issue_label") {
		t.Fatal("pr-only mode should not enable issue_label")
	}
}

func TestGooseSessionTaskKeyStableAndBounded(t *testing.T) {
	t.Parallel()

	a := GooseSessionTaskKey("Owner/Repo", "owner/repo#123")
	b := GooseSessionTaskKey("Owner/Repo", "owner/repo#123")
	c := GooseSessionTaskKey("Owner/Repo", "owner/repo#124")

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

func TestGooseSessionNameBounded(t *testing.T) {
	t.Parallel()

	name := GooseSessionName(strings.Repeat("repo-", 20), strings.Repeat("task-", 20))
	if !strings.HasPrefix(name, "rascal-") {
		t.Fatalf("expected name prefix rascal-, got %q", name)
	}
	if len(name) > 63 {
		t.Fatalf("expected name length <= 63, got %d", len(name))
	}
}
