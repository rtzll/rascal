package remote

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

func Script(lines ...string) string {
	out := strings.Join(lines, "\n")
	if strings.TrimSpace(out) == "" {
		return ""
	}
	return strings.TrimRight(out, "\n") + "\n"
}

func ShellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'"'"'`) + "'"
}

func ExpandPath(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", nil
	}
	if strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolve user home directory: %w", err)
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func NormalizedSSHKeyPath(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	expanded, err := ExpandPath(path)
	if err != nil {
		return path
	}
	return expanded
}
