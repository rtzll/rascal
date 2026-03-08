package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestRunRemoteDoctorUsesSlotUnitsOnly(t *testing.T) {
	logDir := setupDoctorRemoteFakes(t)

	status, err := runRemoteDoctor(deployConfig{
		Host:    "example-host",
		SSHUser: "root",
		SSHPort: 22,
	})
	if err != nil {
		t.Fatalf("runRemoteDoctor: %v", err)
	}
	if !status.RascalService {
		t.Fatal("expected rascal service check to pass")
	}
	if status.ActiveSlot != "blue" {
		t.Fatalf("active slot = %q, want blue", status.ActiveSlot)
	}
	if !status.AuthRuntimeSynced {
		t.Fatal("expected auth runtime sync check to pass")
	}
	if !status.RunnerImageConfigured {
		t.Fatal("expected explicit runner image config check to pass")
	}
	if status.RunnerImageGoose != "rascal-runner-goose:latest" {
		t.Fatalf("runner image goose = %q, want rascal-runner-goose:latest", status.RunnerImageGoose)
	}
	if status.RunnerImageCodex != "rascal-runner-codex:latest" {
		t.Fatalf("runner image codex = %q, want rascal-runner-codex:latest", status.RunnerImageCodex)
	}
	if status.RunnerImageGooseID != "sha256:goose123" {
		t.Fatalf("runner image goose id = %q, want sha256:goose123", status.RunnerImageGooseID)
	}
	if status.RunnerImageCodexID != "sha256:codex456" {
		t.Fatalf("runner image codex id = %q, want sha256:codex456", status.RunnerImageCodexID)
	}

	sshLog, err := os.ReadFile(filepath.Join(logDir, "ssh_calls.log"))
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	sshCalls := string(sshLog)
	if !strings.Contains(sshCalls, "systemctl is-active --quiet 'rascal@blue'") {
		t.Fatalf("expected slot unit checks in doctor commands, got:\n%s", sshCalls)
	}
	if containsLegacySingleUnitRef(sshCalls) {
		t.Fatalf("unexpected legacy single-unit command in doctor calls:\n%s", sshCalls)
	}
}

func setupDoctorRemoteFakes(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("RASCAL_DOCTOR_LOG_DIR", logDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeFakeExe(t, filepath.Join(binDir, "ssh"), `#!/usr/bin/env bash
set -euo pipefail
log_dir="${RASCAL_DOCTOR_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/ssh_calls.log"
cmd="${@: -1}"
if [[ "$cmd" == *"case \"\$slot\" in blue|green) echo \"\$slot\" ;;"* ]]; then
  printf 'blue'
  exit 0
fi
if [[ "$cmd" == *"printf 'goose=%s\\ncodex=%s\\n'"* ]]; then
  printf 'goose=rascal-runner-goose:latest\ncodex=rascal-runner-codex:latest\n'
  exit 0
fi
if [[ "$cmd" == *"printf 'goose_id=%s\\n'"* ]]; then
  printf 'goose_id=sha256:goose123\ncodex_id=sha256:codex456\n'
  exit 0
fi
if [[ "$cmd" == *"echo ok"* ]]; then
  printf 'ok'
  exit 0
fi
exit 0
`)

	return logDir
}

func containsLegacySingleUnitRef(s string) bool {
	legacyPattern := regexp.MustCompile(`\bsystemctl\s+(?:is-active --quiet|show|restart|stop|disable)\s+rascal(?:\s|;|$)`)
	return legacyPattern.MatchString(s)
}
