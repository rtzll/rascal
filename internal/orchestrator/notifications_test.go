package orchestrator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCleanupStaleAgentSessionDirs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "goose-sessions")
	oldDir := filepath.Join(root, "old")
	freshDir := filepath.Join(root, "fresh")
	for _, dir := range []string{oldDir, freshDir} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatalf("MkdirAll(%s) error = %v", dir, err)
		}
	}

	now := time.Now().UTC()
	if err := os.Chtimes(oldDir, now.AddDate(0, 0, -30), now.AddDate(0, 0, -30)); err != nil {
		t.Fatalf("Chtimes(oldDir) error = %v", err)
	}
	if err := os.Chtimes(freshDir, now.AddDate(0, 0, -2), now.AddDate(0, 0, -2)); err != nil {
		t.Fatalf("Chtimes(freshDir) error = %v", err)
	}

	removed, err := CleanupStaleAgentSessionDirs(root, 14, now)
	if err != nil {
		t.Fatalf("CleanupStaleAgentSessionDirs() error = %v", err)
	}
	if removed != 1 {
		t.Fatalf("removed = %d, want 1", removed)
	}
	if _, err := os.Stat(oldDir); !os.IsNotExist(err) {
		t.Fatalf("oldDir stat err = %v, want not exist", err)
	}
	if _, err := os.Stat(freshDir); err != nil {
		t.Fatalf("freshDir should remain: %v", err)
	}
}

func TestParseUsageLimitRetryAtSupportsAbsoluteTimestampWithZone(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "Request failed: You've hit your usage limit. Try again at Mar 10th, 2026 6:31 AM UTC."

	retryAt, reason := ParseUsageLimitRetryAt(corpus, now)

	expected := time.Date(2026, time.March, 10, 6, 31, 0, 0, time.UTC)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
	if !strings.Contains(reason, "Mar 10, 2026 6:31 AM UTC") {
		t.Fatalf("reason = %q, want normalized absolute timestamp", reason)
	}
}

func TestParseUsageLimitRetryAtSupportsRFC3339(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "You've hit your usage limit. Try again at 2026-03-10T06:31:00Z."

	retryAt, _ := ParseUsageLimitRetryAt(corpus, now)

	expected := time.Date(2026, time.March, 10, 6, 31, 0, 0, time.UTC)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
}

func TestParseUsageLimitRetryAtSupportsRelativeDelay(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.March, 8, 12, 0, 0, 0, time.UTC)
	corpus := "You've hit your usage limit. Please try again in 2 hours 15 minutes."

	retryAt, reason := ParseUsageLimitRetryAt(corpus, now)

	expected := now.Add(2*time.Hour + 15*time.Minute)
	if !retryAt.Equal(expected) {
		t.Fatalf("retryAt = %s, want %s", retryAt, expected)
	}
	if !strings.Contains(reason, "2 hours 15 minutes") {
		t.Fatalf("reason = %q, want relative delay hint", reason)
	}
}
