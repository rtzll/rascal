package buildinfo

import "testing"

func TestBinaryVersion(t *testing.T) {
	origVersion, origCommit, origDate := Version, Commit, Date
	t.Cleanup(func() {
		Version, Commit, Date = origVersion, origCommit, origDate
	})

	Version = "v1.2.3"
	Commit = "abcdef0"
	Date = "2026-03-03T12:00:00Z"

	if got, want := Summary(), "version=v1.2.3 commit=abcdef0 built=2026-03-03T12:00:00Z"; got != want {
		t.Fatalf("Summary() = %q, want %q", got, want)
	}
	if got, want := BinaryVersion("rascal"), "rascal v1.2.3 (commit: abcdef0, built: 2026-03-03T12:00:00Z)"; got != want {
		t.Fatalf("BinaryVersion() = %q, want %q", got, want)
	}
}

func TestIsVersionRequest(t *testing.T) {
	t.Parallel()

	for _, args := range [][]string{{"--version"}, {"-version"}, {"version"}} {
		if !IsVersionRequest(args) {
			t.Fatalf("expected version request for %v", args)
		}
	}
	for _, args := range [][]string{{}, {"serve"}, {"--version", "--json"}} {
		if IsVersionRequest(args) {
			t.Fatalf("unexpected version request for %v", args)
		}
	}
}

func TestLinkerFlags(t *testing.T) {
	t.Parallel()

	got := LinkerFlags("v1.2.3", "abcdef0", "2026-03-03T12:00:00Z", true)
	want := "-s -w -X github.com/rtzll/rascal/internal/buildinfo.Version=v1.2.3 -X github.com/rtzll/rascal/internal/buildinfo.Commit=abcdef0 -X github.com/rtzll/rascal/internal/buildinfo.Date=2026-03-03T12:00:00Z"
	if got != want {
		t.Fatalf("LinkerFlags() = %q, want %q", got, want)
	}
}
