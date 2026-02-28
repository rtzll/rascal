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
