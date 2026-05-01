package remote

import "fmt"

func SCPArgs(cfg SSHConfig, source, target string) []string {
	args := []string{
		"-P", fmt.Sprintf("%d", cfg.normalizedPort()),
	}
	args = append(args, commonSSHOptions()...)
	if keyPath := NormalizedSSHKeyPath(cfg.KeyPath); keyPath != "" {
		args = append(args, "-i", keyPath)
	}
	args = append(args, source, target)
	return args
}
