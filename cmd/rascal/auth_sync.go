package main

import (
	"fmt"
	"os"
	"strings"
)

type syncRemoteAuthConfig struct {
	Host          string
	SSHUser       string
	SSHKeyPath    string
	SSHPort       int
	APIToken      string
	GitHubRuntime string
	WebhookSecret string
	CodexAuthPath string
	Restart       bool
}

func syncRemoteAuth(cfg syncRemoteAuthConfig) error {
	cfg.Host = strings.TrimSpace(cfg.Host)
	cfg.SSHUser = firstNonEmpty(strings.TrimSpace(cfg.SSHUser), "root")
	cfg.SSHKeyPath = strings.TrimSpace(cfg.SSHKeyPath)
	if cfg.SSHPort <= 0 {
		cfg.SSHPort = 22
	}
	cfg.APIToken = strings.TrimSpace(cfg.APIToken)
	cfg.GitHubRuntime = strings.TrimSpace(cfg.GitHubRuntime)
	cfg.WebhookSecret = strings.TrimSpace(cfg.WebhookSecret)
	cfg.CodexAuthPath = strings.TrimSpace(cfg.CodexAuthPath)

	if cfg.Host == "" {
		return fmt.Errorf("host is required")
	}

	syncCodex := cfg.CodexAuthPath != ""
	syncServerAuth := cfg.APIToken != "" || cfg.GitHubRuntime != "" || cfg.WebhookSecret != "" || !syncCodex
	if syncServerAuth && (cfg.APIToken == "" || cfg.GitHubRuntime == "" || cfg.WebhookSecret == "") {
		return fmt.Errorf("api token, github runtime token, and webhook secret are required")
	}
	if syncServerAuth {
		for _, value := range []string{cfg.APIToken, cfg.GitHubRuntime, cfg.WebhookSecret} {
			if strings.Contains(value, "\n") || strings.Contains(value, "\r") {
				return fmt.Errorf("auth values must not contain newlines")
			}
		}
	}

	deploy := deployConfig{
		Host:       cfg.Host,
		SSHUser:    cfg.SSHUser,
		SSHKeyPath: cfg.SSHKeyPath,
		SSHPort:    cfg.SSHPort,
	}
	if err := runLocal("ssh", sshArgs(deploy, "mkdir -p /tmp/rascal-bootstrap /etc/rascal")...); err != nil {
		return err
	}

	steps := []string{"set -euo pipefail"}

	if syncServerAuth {
		tmpFile, err := os.CreateTemp("", "rascal-auth-update-*.env")
		if err != nil {
			return fmt.Errorf("create temp auth file: %w", err)
		}
		defer os.Remove(tmpFile.Name())

		content := fmt.Sprintf(
			"RASCAL_API_TOKEN=%s\nRASCAL_GITHUB_TOKEN=%s\nRASCAL_GITHUB_WEBHOOK_SECRET=%s\n",
			cfg.APIToken,
			cfg.GitHubRuntime,
			cfg.WebhookSecret,
		)
		if _, err := tmpFile.WriteString(content); err != nil {
			_ = tmpFile.Close()
			return fmt.Errorf("write temp auth file: %w", err)
		}
		if err := tmpFile.Close(); err != nil {
			return fmt.Errorf("close temp auth file: %w", err)
		}
		if err := runLocal("scp", scpArgs(deploy, tmpFile.Name(), remoteTarget(deploy, "/tmp/rascal-bootstrap/auth.env.update"))...); err != nil {
			return err
		}

		steps = append(steps,
			"touch /etc/rascal/rascal.env",
			"chmod 600 /etc/rascal/rascal.env",
			"awk -F= 'NR==FNR {u[$1]=$0; next} !($1 in u) {print $0} END {for (k in u) print u[k]}' /tmp/rascal-bootstrap/auth.env.update /etc/rascal/rascal.env > /tmp/rascal-bootstrap/rascal.env.merged",
			"install -m 0600 /tmp/rascal-bootstrap/rascal.env.merged /etc/rascal/rascal.env",
		)
	}

	if syncCodex {
		expandedPath, err := expandPath(cfg.CodexAuthPath)
		if err != nil {
			return fmt.Errorf("invalid codex auth path: %w", err)
		}
		if _, err := os.Stat(expandedPath); err != nil {
			return fmt.Errorf("codex auth file is required at %s: %w", expandedPath, err)
		}
		if err := runLocal("scp", scpArgs(deploy, expandedPath, remoteTarget(deploy, "/tmp/rascal-bootstrap/auth.json"))...); err != nil {
			return err
		}
		steps = append(steps, "install -m 0600 /tmp/rascal-bootstrap/auth.json /etc/rascal/codex_auth.json")
	}

	if cfg.Restart {
		steps = append(steps,
			"slot=$(tr -d '[:space:]' </etc/rascal/active_slot 2>/dev/null || true)",
			"case \"$slot\" in blue|green) ;; *) if systemctl is-active --quiet 'rascal@blue'; then slot=blue; elif systemctl is-active --quiet 'rascal@green'; then slot=green; else slot=blue; fi ;; esac",
			"systemctl restart \"rascal@$slot\"",
		)
	}
	return runLocal("ssh", sshArgs(deploy, strings.Join(steps, " && "))...)
}
