package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"
)

type remoteDoctorStatus struct {
	Host               string `json:"host"`
	RascalService      bool   `json:"rascal_service"`
	ActiveSlot         string `json:"active_slot,omitempty"`
	DockerInstalled    bool   `json:"docker_installed"`
	SQLiteInstalled    bool   `json:"sqlite_installed"`
	CaddyInstalled     bool   `json:"caddy_installed"`
	EnvFilePresent     bool   `json:"env_file_present"`
	AuthRuntimeSynced  bool   `json:"auth_runtime_synced"`
	CodexAuthPresent   bool   `json:"codex_auth_present"`
	RunnerImagePresent bool   `json:"runner_image_present"`
}

func runRemoteDoctor(cfg deployConfig) (remoteDoctorStatus, error) {
	status := remoteDoctorStatus{Host: cfg.Host}
	if strings.TrimSpace(cfg.Host) == "" {
		return status, fmt.Errorf("host is required")
	}

	check := func(expr string) bool {
		out, err := runLocalCapture("ssh", sshArgs(cfg, expr)...)
		if err != nil {
			return false
		}
		return strings.TrimSpace(out) == "ok"
	}

	status.RascalService = check("if systemctl is-active --quiet 'rascal@blue' || systemctl is-active --quiet 'rascal@green' || systemctl is-active --quiet rascal; then echo ok; fi")
	activeSlot, _ := runLocalCapture("ssh", sshArgs(cfg, strings.Join([]string{
		"set -eu",
		"slot=''",
		"if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi",
		"case \"$slot\" in blue|green) echo \"$slot\" ;;",
		"*) if systemctl is-active --quiet 'rascal@blue'; then echo blue; elif systemctl is-active --quiet 'rascal@green'; then echo green; elif systemctl is-active --quiet rascal; then echo legacy; fi ;; esac",
	}, "\n"))...)
	status.ActiveSlot = strings.TrimSpace(activeSlot)
	status.DockerInstalled = check("command -v docker >/dev/null 2>&1 && echo ok")
	status.SQLiteInstalled = check("command -v sqlite3 >/dev/null 2>&1 && echo ok")
	status.CaddyInstalled = check("command -v caddy >/dev/null 2>&1 && echo ok")
	status.EnvFilePresent = check("[ -f /etc/rascal/rascal.env ] && echo ok")
	status.AuthRuntimeSynced = check(strings.Join([]string{
		"set -eu",
		"env_epoch=$(stat -c %Y /etc/rascal/rascal.env 2>/dev/null || echo 0)",
		"slot=''",
		"if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi",
		"case \"$slot\" in blue|green) ;;",
		"*) if systemctl is-active --quiet 'rascal@blue'; then slot=blue; elif systemctl is-active --quiet 'rascal@green'; then slot=green; elif systemctl is-active --quiet rascal; then slot=legacy; else slot=''; fi ;; esac",
		`[ -n "$slot" ]`,
		`if [ "$slot" = "legacy" ]; then svc_ts=$(systemctl show rascal -p ExecMainStartTimestamp --value 2>/dev/null || true); else svc_ts=$(systemctl show "rascal@$slot" -p ExecMainStartTimestamp --value 2>/dev/null || true); fi`,
		`[ -n "$svc_ts" ]`,
		`svc_epoch=$(date -d "$svc_ts" +%s 2>/dev/null || echo 0)`,
		`[ "$svc_epoch" -ge "$env_epoch" ]`,
		"echo ok",
	}, "\n"))
	status.CodexAuthPresent = check("[ -f /etc/rascal/codex_auth.json ] && echo ok")
	status.RunnerImagePresent = check("docker image inspect rascal-runner:latest >/dev/null 2>&1 && echo ok")
	return status, nil
}

func checkServerHealth(baseURL string) (bool, string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return false, "missing server_url"
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{"/readyz", "/healthz"} {
		req, err := http.NewRequest(http.MethodGet, baseURL+path, nil)
		if err != nil {
			return false, err.Error()
		}
		resp, err := client.Do(req)
		if err != nil {
			if path == "/healthz" {
				return false, err.Error()
			}
			continue
		}
		_ = resp.Body.Close()
		if resp.StatusCode >= 300 {
			if path == "/healthz" {
				return false, fmt.Sprintf("status %d", resp.StatusCode)
			}
			continue
		}
		return true, ""
	}
	return false, "server health check failed"
}

func waitForServerHealth(baseURL string, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr string
	for time.Now().Before(deadline) {
		ok, errText := checkServerHealth(baseURL)
		if ok {
			return nil
		}
		lastErr = errText
		time.Sleep(2 * time.Second)
	}
	if strings.TrimSpace(lastErr) == "" {
		lastErr = "timed out waiting for server health check"
	}
	return fmt.Errorf("%s", lastErr)
}

func checkServerHealthSSH(cfg deployConfig) (bool, string) {
	checkCmd := strings.Join([]string{
		"set -u",
		"  if command -v curl >/dev/null 2>&1; then",
		"    if curl -fsS --max-time 5 http://127.0.0.1:8080/readyz >/dev/null 2>&1 || curl -fsS --max-time 5 http://127.0.0.1:8080/healthz >/dev/null 2>&1; then",
		"      echo ok",
		"      exit 0",
		"    fi",
		"  fi",
		"  if command -v wget >/dev/null 2>&1; then",
		"    if wget -q -T 5 -O - http://127.0.0.1:8080/readyz >/dev/null 2>&1 || wget -q -T 5 -O - http://127.0.0.1:8080/healthz >/dev/null 2>&1; then",
		"      echo ok",
		"      exit 0",
		"    fi",
		"  fi",
		"if systemctl is-active --quiet 'rascal@blue' || systemctl is-active --quiet 'rascal@green' || systemctl is-active --quiet rascal; then",
		"  echo ok",
		"  exit 0",
		"fi",
		"exit 1",
	}, "\n")
	out, err := runLocalCapture("ssh", sshArgs(cfg, checkCmd)...)
	if err != nil {
		return false, err.Error()
	}
	if strings.TrimSpace(out) != "ok" {
		return false, "remote health probe did not return ok"
	}
	return true, ""
}

func waitForServerHealthSSH(cfg deployConfig, timeout time.Duration) error {
	if timeout <= 0 {
		timeout = 60 * time.Second
	}
	deadline := time.Now().Add(timeout)
	var lastErr string
	for time.Now().Before(deadline) {
		ok, errText := checkServerHealthSSH(cfg)
		if ok {
			return nil
		}
		lastErr = errText
		time.Sleep(2 * time.Second)
	}
	if strings.TrimSpace(lastErr) == "" {
		lastErr = "timed out waiting for server health check"
	}
	return fmt.Errorf("%s", lastErr)
}

func remoteCaddyDomainConfigured(cfg deployConfig, domain string) (bool, error) {
	domain = strings.TrimSpace(domain)
	if domain == "" {
		return true, nil
	}
	checkCmd := strings.Join([]string{
		"set -eu",
		"[ -f /etc/caddy/Caddyfile ]",
		"grep -Fqs -- " + shellSingleQuote(domain) + " /etc/caddy/Caddyfile",
		"echo ok",
	}, "\n")
	out, err := runLocalCapture("ssh", sshArgs(cfg, checkCmd)...)
	if err != nil {
		return false, err
	}
	return strings.TrimSpace(out) == "ok", nil
}
