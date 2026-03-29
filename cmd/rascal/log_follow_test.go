package main

import (
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/api"
	"github.com/rtzll/rascal/internal/state"
)

func TestStreamRunLogsFollowAppendsOnlyDiff(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "alpha\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "alpha\nbeta\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "alpha\nbeta\ngamma\n", RunStatus: state.StatusSucceeded, Done: true},
	}

	a, closeServer, _ := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_abc123", true, 1*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "alpha\nbeta\ngamma\n" {
		t.Fatalf("unexpected follow output:\n--- got ---\n%s\n--- want ---\n%s", out, "alpha\nbeta\ngamma\n")
	}
}

func TestStreamRunLogsFollowPrintsFullBodyOnReset(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "one\ntwo\n", RunStatus: state.StatusRunning, Done: false},
		{Logs: "reset\n", RunStatus: state.StatusRunning, Done: true},
	}

	a, closeServer, _ := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_reset", true, 1*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "one\ntwo\nreset\n" {
		t.Fatalf("unexpected follow output on reset:\n--- got ---\n%s\n--- want ---\n%s", out, "one\ntwo\nreset\n")
	}
}

func TestStreamRunLogsFollowStopsAfterDone(t *testing.T) {
	responses := []api.RunLogsResponse{
		{Logs: "done-now\n", RunStatus: state.StatusFailed, Done: true},
	}

	a, closeServer, requestCount := newFollowLogsTestApp(t, responses)
	defer closeServer()

	out, err := captureStdout(func() error {
		return a.streamRunLogs("run_done", true, 5*time.Millisecond, 0, 200)
	})
	if err != nil {
		t.Fatalf("streamRunLogs follow: %v", err)
	}
	if out != "done-now\n" {
		t.Fatalf("unexpected output:\n--- got ---\n%s\n--- want ---\n%s", out, "done-now\n")
	}
	if got := requestCount(); got != 1 {
		t.Fatalf("expected exactly one follow request when done=true, got %d", got)
	}
}
