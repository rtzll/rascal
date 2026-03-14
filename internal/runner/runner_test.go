package runner

import "testing"

func TestNormalizeMode(t *testing.T) {
	t.Parallel()

	tests := map[string]Mode{
		"":        ModeNoop,
		"noop":    ModeNoop,
		"docker":  ModeDocker,
		"DOCKER":  ModeDocker,
		"unknown": ModeNoop,
	}
	for in, want := range tests {
		if got := NormalizeMode(in); got != want {
			t.Fatalf("NormalizeMode(%q) = %q, want %q", in, got, want)
		}
	}
}
