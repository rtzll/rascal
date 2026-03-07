package runner

import (
	"encoding/json"
	"fmt"
	"os"
)

// Meta mirrors /rascal-meta/meta.json produced by the container entrypoint.
type Meta struct {
	RunID          string `json:"run_id"`
	TaskID         string `json:"task_id"`
	Repo           string `json:"repo"`
	BaseBranch     string `json:"base_branch"`
	HeadBranch     string `json:"head_branch"`
	PRNumber       int    `json:"pr_number"`
	PRURL          string `json:"pr_url"`
	HeadSHA        string `json:"head_sha"`
	AgentSessionID string `json:"agent_session_id,omitempty"`
	ExitCode       int    `json:"exit_code"`
	Error          string `json:"error,omitempty"`
}

func ReadMeta(path string) (Meta, error) {
	var m Meta
	data, err := os.ReadFile(path)
	if err != nil {
		return Meta{}, fmt.Errorf("read meta file: %w", err)
	}
	if err := json.Unmarshal(data, &m); err != nil {
		return Meta{}, fmt.Errorf("decode meta file: %w", err)
	}
	return m, nil
}

func WriteMeta(path string, m Meta) error {
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("encode meta file: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write meta file: %w", err)
	}
	return nil
}
