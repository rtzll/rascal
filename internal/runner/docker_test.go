package runner

import (
	"strings"
	"testing"
)

func TestSanitizeContainerName(t *testing.T) {
	t.Parallel()

	got := sanitizeContainerName("Rascal/Run:ABC 123")
	if got != "rascal-run-abc-123" {
		t.Fatalf("unexpected sanitized name: %q", got)
	}

	longIn := strings.Repeat("x", 80)
	longOut := sanitizeContainerName(longIn)
	if len(longOut) > 63 {
		t.Fatalf("expected max 63 chars, got %d", len(longOut))
	}
}
