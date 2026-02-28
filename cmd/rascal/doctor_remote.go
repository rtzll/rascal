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
	DockerInstalled    bool   `json:"docker_installed"`
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

	status.RascalService = check("systemctl is-active --quiet rascal && echo ok")
	status.DockerInstalled = check("command -v docker >/dev/null 2>&1 && echo ok")
	status.CaddyInstalled = check("command -v caddy >/dev/null 2>&1 && echo ok")
	status.EnvFilePresent = check("[ -f /etc/rascal/rascal.env ] && echo ok")
	status.AuthRuntimeSynced = check(strings.Join([]string{
		"set -eu",
		"env_epoch=$(stat -c %Y /etc/rascal/rascal.env 2>/dev/null || echo 0)",
		`svc_ts=$(systemctl show rascal -p ExecMainStartTimestamp --value 2>/dev/null || true)`,
		`[ -n "$svc_ts" ]`,
		`svc_epoch=$(date -d "$svc_ts" +%s 2>/dev/null || echo 0)`,
		`[ "$svc_epoch" -ge "$env_epoch" ]`,
		"echo ok",
	}, " && "))
	status.CodexAuthPresent = check("[ -f /etc/rascal/codex_auth.json ] && echo ok")
	status.RunnerImagePresent = check("docker image inspect rascal-runner:latest >/dev/null 2>&1 && echo ok")
	return status, nil
}

func checkServerHealth(baseURL string) (bool, string) {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return false, "missing server_url"
	}
	req, err := http.NewRequest(http.MethodGet, baseURL+"/healthz", nil)
	if err != nil {
		return false, err.Error()
	}
	client := &http.Client{Timeout: 5 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return false, err.Error()
	}
	_ = resp.Body.Close()
	if resp.StatusCode >= 300 {
		return false, fmt.Sprintf("status %d", resp.StatusCode)
	}
	return true, ""
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
		"set -eu",
		"if command -v curl >/dev/null 2>&1; then",
		"  curl -fsS --max-time 5 http://127.0.0.1:8080/healthz >/dev/null && echo ok",
		"elif command -v wget >/dev/null 2>&1; then",
		"  wget -q -T 5 -O - http://127.0.0.1:8080/healthz >/dev/null && echo ok",
		"else",
		"  systemctl is-active --quiet rascal && echo ok",
		"fi",
	}, "\n")
	out, err := runLocalCapture("ssh", sshArgs(cfg, checkCmd)...)
	if err != nil {
		return false, err.Error()
	}
	return strings.TrimSpace(out) == "ok", ""
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
