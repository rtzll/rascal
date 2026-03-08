package main

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRascaldJournalctlRemoteCmdUsesSlotUnitsOnly(t *testing.T) {
	cmd := rascaldJournalctlRemoteCmd(120, true)
	if !strings.Contains(cmd, `blue|green) unit="rascal@$slot"`) {
		t.Fatalf("expected slot-based unit selection, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, `systemctl is-active --quiet 'rascal@green'`) {
		t.Fatalf("expected slot service fallback for green unit, got:\n%s", cmd)
	}
	if !strings.Contains(cmd, `systemctl is-active --quiet 'rascal@blue'`) {
		t.Fatalf("expected slot service fallback for blue unit, got:\n%s", cmd)
	}
	if strings.Contains(cmd, `unit=rascal`) && !strings.Contains(cmd, `unit=rascal@`) {
		t.Fatalf("unexpected legacy unit selection in command:\n%s", cmd)
	}
	if containsLegacySingleUnitRef(cmd) {
		t.Fatalf("unexpected legacy single-unit checks in command:\n%s", cmd)
	}
}

func TestAPIDoOverSSHUsesSlotPortsWithoutLegacySingleUnitChecks(t *testing.T) {
	logDir := setupAPISSHFakes(t)

	client := apiClient{
		transport: "ssh",
		sshHost:   "example-host",
		sshUser:   "root",
		sshPort:   22,
	}
	resp, err := client.doOverSSH(http.MethodGet, "/v1/healthz", nil)
	if err != nil {
		t.Fatalf("doOverSSH: %v", err)
	}
	if err := resp.Body.Close(); err != nil {
		t.Fatalf("close body: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status code = %d, want 200", resp.StatusCode)
	}

	sshLog, err := os.ReadFile(filepath.Join(logDir, "ssh_calls.log"))
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	sshCalls := string(sshLog)
	if !strings.Contains(sshCalls, `systemctl is-active --quiet 'rascal@green'`) {
		t.Fatalf("expected slot health fallback for green in ssh command, got:\n%s", sshCalls)
	}
	if !strings.Contains(sshCalls, `systemctl is-active --quiet 'rascal@blue'`) {
		t.Fatalf("expected slot health fallback for blue in ssh command, got:\n%s", sshCalls)
	}
	if strings.Contains(sshCalls, "127.0.0.1:8080") {
		t.Fatalf("unexpected proxy-port fallback in ssh API command, got:\n%s", sshCalls)
	}
	if containsLegacySingleUnitRef(sshCalls) {
		t.Fatalf("unexpected legacy single-unit checks in ssh API command, got:\n%s", sshCalls)
	}
}

func setupAPISSHFakes(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("RASCAL_API_SSH_LOG_DIR", logDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeFakeExe(t, filepath.Join(binDir, "ssh"), `#!/usr/bin/env bash
set -euo pipefail
log_dir="${RASCAL_API_SSH_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/ssh_calls.log"
cat <<'EOF'
HTTP/1.1 200 OK
Content-Type: application/json
Content-Length: 2

{}
EOF
`)

	return logDir
}
