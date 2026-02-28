package runner

import (
	"context"
	"errors"
)

// Launcher starts a run inside an execution environment (Docker in v1).
type Launcher interface {
	Start(ctx context.Context, runID string, runDir string, env map[string]string) error
}

// NoopLauncher is a safe default for scaffolding and local development.
type NoopLauncher struct{}

func (NoopLauncher) Start(_ context.Context, _ string, _ string, _ map[string]string) error {
	return errors.New("runner not configured yet")
}
