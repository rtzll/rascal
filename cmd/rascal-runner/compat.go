package main

import (
	"fmt"
	"time"

	"github.com/rtzll/rascal/internal/worker"
)

type config = worker.Config
type commandExecutor = worker.CommandExecutor

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func buildInfoSummary() string {
	syncBuildInfo()
	return worker.BuildInfoSummary()
}

func run() error {
	syncBuildInfo()
	if err := worker.Run(); err != nil {
		return fmt.Errorf("run worker: %w", err)
	}
	return nil
}

func runWithExecutor(ex commandExecutor) error {
	syncBuildInfo()
	if err := worker.RunWithExecutor(ex); err != nil {
		return fmt.Errorf("run worker with executor: %w", err)
	}
	return nil
}

func loadConfig() (config, error) {
	cfg, err := worker.LoadConfig()
	if err != nil {
		return config{}, fmt.Errorf("load config: %w", err)
	}
	return cfg, nil
}

func runStage(name string, fn func() error) error {
	if err := worker.RunStage(name, fn); err != nil {
		return fmt.Errorf("run stage %s: %w", name, err)
	}
	return nil
}

func taskSubject(task, fallback string) string {
	return worker.TaskSubject(task, fallback)
}

func isConventionalTitle(title string) bool {
	return worker.IsConventionalTitle(title)
}

func loadAgentCommitMessage(path string) (string, string, error) {
	title, body, err := worker.LoadAgentCommitMessage(path)
	if err != nil {
		return "", "", fmt.Errorf("load agent commit message: %w", err)
	}
	return title, body, nil
}

func normalizeRepoLocalMetaArtifacts(cfg config) error {
	if err := worker.NormalizeRepoLocalMetaArtifacts(cfg); err != nil {
		return fmt.Errorf("normalize repo local meta artifacts: %w", err)
	}
	return nil
}

func runGoose(ex commandExecutor, cfg config) (string, string, error) {
	output, sessionID, err := worker.RunGoose(ex, cfg)
	if err != nil {
		return "", "", fmt.Errorf("run goose: %w", err)
	}
	return output, sessionID, nil
}

func runCodex(ex commandExecutor, cfg config) (string, string, error) {
	output, sessionID, err := worker.RunCodex(ex, cfg)
	if err != nil {
		return "", "", fmt.Errorf("run codex: %w", err)
	}
	return output, sessionID, nil
}

func resetGooseSessionRoot(path string) error {
	if err := worker.ResetGooseSessionRoot(path); err != nil {
		return fmt.Errorf("reset goose session root: %w", err)
	}
	return nil
}

func isSessionResumeFailure(err error, logPath string) bool {
	return worker.IsSessionResumeFailure(err, logPath)
}
