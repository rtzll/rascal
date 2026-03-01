package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSyncRemoteAuthValidation(t *testing.T) {
	t.Run("missing host", func(t *testing.T) {
		err := syncRemoteAuth(syncRemoteAuthConfig{
			APIToken:      "api",
			GitHubRuntime: "runtime",
			WebhookSecret: "secret",
		})
		if err == nil || !strings.Contains(err.Error(), "host is required") {
			t.Fatalf("expected missing host error, got: %v", err)
		}
	})

	t.Run("missing auth values", func(t *testing.T) {
		err := syncRemoteAuth(syncRemoteAuthConfig{Host: "example-host"})
		if err == nil || !strings.Contains(err.Error(), "api token, github runtime token, and webhook secret are required") {
			t.Fatalf("expected missing auth values error, got: %v", err)
		}
	})

	t.Run("rejects newlines", func(t *testing.T) {
		err := syncRemoteAuth(syncRemoteAuthConfig{
			Host:          "example-host",
			APIToken:      "api\nbad",
			GitHubRuntime: "runtime",
			WebhookSecret: "secret",
		})
		if err == nil || !strings.Contains(err.Error(), "must not contain newlines") {
			t.Fatalf("expected newline rejection error, got: %v", err)
		}
	})
}

func TestSyncRemoteAuthUploadsEnvUpdateWithoutRestart(t *testing.T) {
	logDir := setupSyncCommandFakes(t)

	err := syncRemoteAuth(syncRemoteAuthConfig{
		Host:          "example-host",
		APIToken:      "api-token",
		GitHubRuntime: "runtime-token",
		WebhookSecret: "webhook-secret",
		Restart:       false,
	})
	if err != nil {
		t.Fatalf("syncRemoteAuth: %v", err)
	}

	payloadPath := filepath.Join(logDir, "scp_payload.env")
	payload, err := os.ReadFile(payloadPath)
	if err != nil {
		t.Fatalf("read uploaded payload: %v", err)
	}
	gotPayload := string(payload)
	for _, want := range []string{
		"RASCAL_API_TOKEN=api-token",
		"RASCAL_GITHUB_TOKEN=runtime-token",
		"RASCAL_GITHUB_WEBHOOK_SECRET=webhook-secret",
	} {
		if !strings.Contains(gotPayload, want) {
			t.Fatalf("uploaded payload missing %q:\n%s", want, gotPayload)
		}
	}

	sshLog, err := os.ReadFile(filepath.Join(logDir, "ssh_calls.log"))
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	sshCalls := string(sshLog)
	if !strings.Contains(sshCalls, "mkdir -p /tmp/rascal-bootstrap /etc/rascal") {
		t.Fatalf("expected remote mkdir command, got:\n%s", sshCalls)
	}
	if !strings.Contains(sshCalls, "awk -F=") {
		t.Fatalf("expected remote env merge command, got:\n%s", sshCalls)
	}
	if strings.Contains(sshCalls, `systemctl restart "rascal@$slot"`) {
		t.Fatalf("did not expect restart command when Restart=false, got:\n%s", sshCalls)
	}
}

func TestSyncRemoteAuthIncludesRestartWhenEnabled(t *testing.T) {
	logDir := setupSyncCommandFakes(t)

	err := syncRemoteAuth(syncRemoteAuthConfig{
		Host:          "example-host",
		SSHUser:       "ubuntu",
		SSHPort:       2222,
		APIToken:      "api-token",
		GitHubRuntime: "runtime-token",
		WebhookSecret: "webhook-secret",
		Restart:       true,
	})
	if err != nil {
		t.Fatalf("syncRemoteAuth: %v", err)
	}

	sshLog, err := os.ReadFile(filepath.Join(logDir, "ssh_calls.log"))
	if err != nil {
		t.Fatalf("read ssh log: %v", err)
	}
	sshCalls := string(sshLog)
	if !strings.Contains(sshCalls, "-p 2222") {
		t.Fatalf("expected ssh port override in calls, got:\n%s", sshCalls)
	}
	if !strings.Contains(sshCalls, "ubuntu@example-host") {
		t.Fatalf("expected ssh user/host target in calls, got:\n%s", sshCalls)
	}
	if !strings.Contains(sshCalls, `systemctl restart "rascal@$slot"`) {
		t.Fatalf("expected restart command when Restart=true, got:\n%s", sshCalls)
	}
}

func setupSyncCommandFakes(t *testing.T) string {
	t.Helper()

	binDir := t.TempDir()
	logDir := t.TempDir()
	t.Setenv("RASCAL_SYNC_LOG_DIR", logDir)
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	writeFakeExe(t, filepath.Join(binDir, "ssh"), `#!/usr/bin/env bash
set -euo pipefail
log_dir="${RASCAL_SYNC_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/ssh_calls.log"
exit 0
`)

	writeFakeExe(t, filepath.Join(binDir, "scp"), `#!/usr/bin/env bash
set -euo pipefail
log_dir="${RASCAL_SYNC_LOG_DIR:?}"
printf '%s\n' "$*" >> "$log_dir/scp_calls.log"
src="${@: -2:1}"
cp "$src" "$log_dir/scp_payload.env"
exit 0
`)

	return logDir
}

func writeFakeExe(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o755); err != nil {
		t.Fatalf("write fake executable %s: %v", path, err)
	}
}

