package orchestrator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/rtzll/rascal/internal/runner"
)

func TestCleanupRunSecretsBestEffortRemovesSecretsDir(t *testing.T) {
	t.Parallel()

	runDir := filepath.Join(t.TempDir(), "run")
	secretsDir := runner.SecretsDir(runDir)
	if err := os.MkdirAll(secretsDir, 0o700); err != nil {
		t.Fatalf("mkdir secrets dir: %v", err)
	}
	if err := os.WriteFile(filepath.Join(secretsDir, "gh_token"), []byte("token"), 0o600); err != nil {
		t.Fatalf("write secret file: %v", err)
	}

	s := &Server{}
	s.cleanupRunSecretsBestEffort("run-1", runDir)

	if _, err := os.Stat(secretsDir); !os.IsNotExist(err) {
		t.Fatalf("expected secrets dir removed, err=%v", err)
	}
}
