package main

import (
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/api"
)

func TestRunLogsFollowRendererAppendsOnlyDiff(t *testing.T) {
	t.Parallel()

	renderer := newRunLogsFollowRenderer(0, time.Now)
	if got := renderer.Render(api.RunLogsResponse{Logs: "alpha\n"}); got != "alpha\n" {
		t.Fatalf("first render = %q, want %q", got, "alpha\n")
	}
	if got := renderer.Render(api.RunLogsResponse{Logs: "alpha\nbeta\n"}); got != "beta\n" {
		t.Fatalf("second render = %q, want %q", got, "beta\n")
	}
	if got := renderer.Render(api.RunLogsResponse{Logs: "alpha\nbeta\ngamma\n"}); got != "gamma\n" {
		t.Fatalf("third render = %q, want %q", got, "gamma\n")
	}
}

func TestRunLogsFollowRendererPrintsFullBodyOnReset(t *testing.T) {
	t.Parallel()

	renderer := newRunLogsFollowRenderer(0, time.Now)
	_ = renderer.Render(api.RunLogsResponse{Logs: "one\ntwo\n"})
	if got := renderer.Render(api.RunLogsResponse{Logs: "reset\n"}); got != "reset\n" {
		t.Fatalf("reset render = %q, want %q", got, "reset\n")
	}
}

func TestRunLogsFollowRendererAppliesSinceFilter(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 3, 29, 12, 0, 0, 0, time.UTC)
	renderer := newRunLogsFollowRenderer(1*time.Hour, func() time.Time { return now })

	logs := "[2026-03-29T10:30:00Z] skipped\n[2026-03-29T11:15:00Z] kept\n"
	if got := renderer.Render(api.RunLogsResponse{Logs: logs}); got != "[2026-03-29T11:15:00Z] kept\n" {
		t.Fatalf("since-filtered render = %q", got)
	}
}
