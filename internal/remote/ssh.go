package remote

import (
	"fmt"
	"strings"
)

type SSHConfig struct {
	Host    string
	User    string
	KeyPath string
	Port    int
}

func (c SSHConfig) normalizedPort() int {
	if c.Port > 0 {
		return c.Port
	}
	return 22
}

func (c SSHConfig) normalizedUser() string {
	if user := strings.TrimSpace(c.User); user != "" {
		return user
	}
	return "root"
}

func SSHArgs(cfg SSHConfig, remoteCmd string) []string {
	args := []string{
		"-p", fmt.Sprintf("%d", cfg.normalizedPort()),
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
	}
	if keyPath := NormalizedSSHKeyPath(cfg.KeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, fmt.Sprintf("%s@%s", cfg.normalizedUser(), strings.TrimSpace(cfg.Host)), remoteCmd)
	return args
}

func RemoteTarget(cfg SSHConfig, path string) string {
	return fmt.Sprintf("%s@%s:%s", cfg.normalizedUser(), strings.TrimSpace(cfg.Host), path)
}
