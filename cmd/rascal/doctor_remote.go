package main

import (
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/defaults"
)

type remoteDoctorStatus struct {
	Host                    string `json:"host"`
	RascalService           bool   `json:"rascal_service"`
	ActiveSlot              string `json:"active_slot,omitempty"`
	DockerInstalled         bool   `json:"docker_installed"`
	SQLiteInstalled         bool   `json:"sqlite_installed"`
	CaddyInstalled          bool   `json:"caddy_installed"`
	EnvFilePresent          bool   `json:"env_file_present"`
	AuthRuntimeSynced       bool   `json:"auth_runtime_synced"`
	RunnerImageConfigured   bool   `json:"runner_image_configured"`
	RunnerImagePresent      bool   `json:"runner_image_present"`
	RunnerImageGooseCodex   string `json:"runner_image_goose,omitempty"`
	RunnerImageCodex        string `json:"runner_image_codex,omitempty"`
	RunnerImagePi           string `json:"runner_image_pi,omitempty"`
	RunnerImageGooseCodexID string `json:"runner_image_goose_id,omitempty"`
	RunnerImageCodexID      string `json:"runner_image_codex_id,omitempty"`
	RunnerImagePiID         string `json:"runner_image_pi_id,omitempty"`
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

	status.RascalService = check("if systemctl is-active --quiet 'rascal@blue' || systemctl is-active --quiet 'rascal@green'; then echo ok; fi")
	activeSlot, err := runLocalCapture("ssh", sshArgs(cfg, strings.Join([]string{
		"set -eu",
		"slot=''",
		"if [ -f /etc/rascal/active_slot ]; then slot=$(tr -d '[:space:]' </etc/rascal/active_slot); fi",
		"case \"$slot\" in blue|green) echo \"$slot\" ;;",
		"*) if systemctl is-active --quiet 'rascal@blue'; then echo blue; elif systemctl is-active --quiet 'rascal@green'; then echo green; fi ;; esac",
	}, "\n"))...)
	if err != nil {
		log.Printf("resolve active slot over SSH failed: %v", err)
	} else {
		status.ActiveSlot = strings.TrimSpace(activeSlot)
	}
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
		"*) if systemctl is-active --quiet 'rascal@blue'; then slot=blue; elif systemctl is-active --quiet 'rascal@green'; then slot=green; else slot=''; fi ;; esac",
		`[ -n "$slot" ]`,
		`svc_ts=$(systemctl show "rascal@$slot" -p ExecMainStartTimestamp --value 2>/dev/null || true)`,
		`[ -n "$svc_ts" ]`,
		`svc_epoch=$(date -d "$svc_ts" +%s 2>/dev/null || echo 0)`,
		`[ "$svc_epoch" -ge "$env_epoch" ]`,
		"echo ok",
	}, "\n"))
	runnerImages, err := runLocalCapture("ssh", sshArgs(cfg, strings.Join([]string{
		"set -eu",
		fmt.Sprintf(`goose_image=%q`, defaults.GooseCodexRunnerImageTag),
		fmt.Sprintf(`codex_image=%q`, defaults.CodexRunnerImageTag),
		fmt.Sprintf(`pi_image=%q`, defaults.PiRunnerImageTag),
		`if [ -f /etc/rascal/rascal.env ]; then`,
		`  set -a`,
		`  . /etc/rascal/rascal.env`,
		`  set +a`,
		`  if [ -n "${RASCAL_RUNNER_IMAGE_GOOSE_CODEX:-}" ]; then goose_image=$RASCAL_RUNNER_IMAGE_GOOSE_CODEX; elif [ -n "${RASCAL_RUNNER_IMAGE_GOOSE:-}" ]; then goose_image=$RASCAL_RUNNER_IMAGE_GOOSE; fi`,
		`  if [ -n "${RASCAL_RUNNER_IMAGE_CODEX:-}" ]; then codex_image=$RASCAL_RUNNER_IMAGE_CODEX; fi`,
		`  if [ -n "${RASCAL_RUNNER_IMAGE_PI:-}" ]; then pi_image=$RASCAL_RUNNER_IMAGE_PI; fi`,
		`fi`,
		`printf 'goose=%s\ncodex=%s\npi=%s\n' "$goose_image" "$codex_image" "$pi_image"`,
	}, "\n"))...)
	if err != nil {
		log.Printf("resolve runner images over SSH failed: %v", err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(runnerImages), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "goose":
				status.RunnerImageGooseCodex = strings.TrimSpace(value)
			case "codex":
				status.RunnerImageCodex = strings.TrimSpace(value)
			case "pi":
				status.RunnerImagePi = strings.TrimSpace(value)
			}
		}
	}
	runnerImageIDs, err := runLocalCapture("ssh", sshArgs(cfg, strings.Join([]string{
		"set -eu",
		`[ -f /etc/rascal/rascal.env ]`,
		`set -a`,
		`. /etc/rascal/rascal.env`,
		`set +a`,
		`goose_image=${RASCAL_RUNNER_IMAGE_GOOSE_CODEX:-${RASCAL_RUNNER_IMAGE_GOOSE:-}}`,
		`codex_image=${RASCAL_RUNNER_IMAGE_CODEX:-}`,
		`pi_image=${RASCAL_RUNNER_IMAGE_PI:-}`,
		`[ -n "$goose_image" ]`,
		`[ -n "$codex_image" ]`,
		`[ -n "$pi_image" ]`,
		`printf 'goose_id=%s\n' "$(docker image inspect -f '{{.Id}}' "$goose_image")"`,
		`printf 'codex_id=%s\n' "$(docker image inspect -f '{{.Id}}' "$codex_image")"`,
		`printf 'pi_id=%s\n' "$(docker image inspect -f '{{.Id}}' "$pi_image")"`,
	}, "\n"))...)
	if err != nil {
		log.Printf("resolve runner image IDs over SSH failed: %v", err)
	} else {
		for _, line := range strings.Split(strings.TrimSpace(runnerImageIDs), "\n") {
			key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
			if !ok {
				continue
			}
			switch strings.TrimSpace(key) {
			case "goose_id":
				status.RunnerImageGooseCodexID = strings.TrimSpace(value)
			case "codex_id":
				status.RunnerImageCodexID = strings.TrimSpace(value)
			case "pi_id":
				status.RunnerImagePiID = strings.TrimSpace(value)
			}
		}
	}
	status.RunnerImageConfigured = check(strings.Join([]string{
		"set -eu",
		`[ -f /etc/rascal/rascal.env ]`,
		`set -a`,
		`. /etc/rascal/rascal.env`,
		`set +a`,
		`[ -n "${RASCAL_RUNNER_IMAGE_GOOSE_CODEX:-${RASCAL_RUNNER_IMAGE_GOOSE:-}}" ]`,
		`[ -n "${RASCAL_RUNNER_IMAGE_CODEX:-}" ]`,
		`[ -n "${RASCAL_RUNNER_IMAGE_PI:-}" ]`,
		"echo ok",
	}, "\n"))
	status.RunnerImagePresent = check(fmt.Sprintf(strings.Join([]string{
		"set -eu",
		`goose_image=%q`,
		`codex_image=%q`,
		`if [ -f /etc/rascal/rascal.env ]; then`,
		`  set -a`,
		`  . /etc/rascal/rascal.env`,
		`  set +a`,
		`  goose_image=${RASCAL_RUNNER_IMAGE_GOOSE_CODEX:-${RASCAL_RUNNER_IMAGE_GOOSE:-}}`,
		`  codex_image=${RASCAL_RUNNER_IMAGE_CODEX:-}`,
		`  pi_image=${RASCAL_RUNNER_IMAGE_PI:-}`,
		`  [ -n "$goose_image" ]`,
		`  [ -n "$codex_image" ]`,
		`  [ -n "$pi_image" ]`,
		`fi`,
		`docker image inspect "$goose_image" "$codex_image" "$pi_image" >/dev/null 2>&1 && echo ok`,
	}, "\n"), defaults.GooseCodexRunnerImageTag, defaults.CodexRunnerImageTag, defaults.PiRunnerImageTag))
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
		if err := resp.Body.Close(); err != nil {
			log.Printf("close remote doctor response body: %v", err)
		}
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
		"if systemctl is-active --quiet 'rascal@blue' || systemctl is-active --quiet 'rascal@green'; then",
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
