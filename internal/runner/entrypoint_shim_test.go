package runner

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestEntrypointIsThinShim(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("failed to resolve current test file path")
	}
	entrypointPath := filepath.Clean(filepath.Join(filepath.Dir(thisFile), "..", "..", "runner", "entrypoint.sh"))
	data, err := os.ReadFile(entrypointPath)
	if err != nil {
		t.Fatalf("read entrypoint script: %v", err)
	}
	script := string(data)

	if !strings.Contains(script, "#!/usr/bin/env bash") {
		t.Fatalf("entrypoint missing bash shebang:\n%s", script)
	}
	if !strings.Contains(script, "set -euo pipefail") {
		t.Fatalf("entrypoint missing strict shell flags:\n%s", script)
	}
	if !strings.Contains(script, "exec /usr/local/bin/rascal-runner") {
		t.Fatalf("entrypoint must exec rascal-runner:\n%s", script)
	}

	// Guard against reintroducing heavy shell orchestration logic.
	for _, forbidden := range []string{"goose ", "gh pr", "git clone", "build_pr_body", "extract_total_tokens"} {
		if strings.Contains(script, forbidden) {
			t.Fatalf("entrypoint should remain a thin shim; found %q:\n%s", forbidden, script)
		}
	}
}

