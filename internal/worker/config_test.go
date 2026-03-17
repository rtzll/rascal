package worker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsurePersistentInstructionsWritesFallbackWhenMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "persistent_instructions.md")
	cfg := Config{PersistentInstructionsPath: path}

	if err := ensurePersistentInstructions(cfg); err != nil {
		t.Fatalf("ensurePersistentInstructions returned error: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read persistent instructions: %v", err)
	}
	text := string(data)
	for _, want := range []string{
		"# Rascal Persistent Instructions",
		"Do not ask for interactive input.",
		"Do not overwrite, revert, or discard user changes you did not make unless the task explicitly requires it.",
		"/rascal-meta/commit_message.txt",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("persistent instructions missing %q\nfull text:\n%s", want, text)
		}
	}
}
