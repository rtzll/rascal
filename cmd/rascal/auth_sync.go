package main

import (
	"errors"
	"fmt"
	"log"
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

	if cfg.Host == "" {
		return fmt.Errorf("host is required")
	}

	if cfg.APIToken == "" || cfg.GitHubRuntime == "" {
		return fmt.Errorf("api token and github runtime token are required")
	}
	for _, value := range []string{cfg.APIToken, cfg.GitHubRuntime, cfg.WebhookSecret} {
		if strings.Contains(value, "\n") || strings.Contains(value, "\r") {
			return fmt.Errorf("auth values must not contain newlines")
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

	tmpFile, err := os.CreateTemp("", "rascal-auth-update-*.env")
	if err != nil {
		return fmt.Errorf("create temp auth file: %w", err)
	}
	defer func() {
		if removeErr := os.Remove(tmpFile.Name()); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			log.Printf("remove temp auth file %s: %v", tmpFile.Name(), removeErr)
		}
	}()

	lines := []string{
		fmt.Sprintf("RASCAL_API_TOKEN=%s", cfg.APIToken),
		fmt.Sprintf("RASCAL_GITHUB_TOKEN=%s", cfg.GitHubRuntime),
	}
	if cfg.WebhookSecret != "" {
		lines = append(lines, fmt.Sprintf("RASCAL_GITHUB_WEBHOOK_SECRET=%s", cfg.WebhookSecret))
	}
	content := strings.Join(lines, "\n") + "\n"
	if _, err := tmpFile.WriteString(content); err != nil {
		if closeErr := tmpFile.Close(); closeErr != nil {
			return fmt.Errorf("write temp auth file: %w (close temp auth file: %v)", err, closeErr)
		}
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
	)
	if cfg.WebhookSecret == "" {
		// Preserve the existing server-side webhook secret when rotating only tokens.
		steps = append(steps,
			`if ! grep -q -E '^RASCAL_GITHUB_WEBHOOK_SECRET=' /etc/rascal/rascal.env; then echo "remote webhook secret missing from /etc/rascal/rascal.env" >&2; exit 1; fi`,
		)
	}
	steps = append(steps,
		"awk -F= 'NR==FNR {u[$1]=$0; next} !($1 in u) {print $0} END {for (k in u) print u[k]}' /tmp/rascal-bootstrap/auth.env.update /etc/rascal/rascal.env > /tmp/rascal-bootstrap/rascal.env.merged",
		"install -m 0600 /tmp/rascal-bootstrap/rascal.env.merged /etc/rascal/rascal.env",
	)

	if cfg.Restart {
		steps = append(steps,
			"slot=$(tr -d '[:space:]' </etc/rascal/active_slot 2>/dev/null || true)",
			"case \"$slot\" in blue|green) ;; *) if systemctl is-active --quiet 'rascal@blue'; then slot=blue; elif systemctl is-active --quiet 'rascal@green'; then slot=green; else slot=blue; fi ;; esac",
			"systemctl restart \"rascal@$slot\"",
		)
	}
	return runLocal("ssh", sshArgs(deploy, strings.Join(steps, " && "))...)
}
