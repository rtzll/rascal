package config

import (
	"bufio"
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerConfig controls rascald runtime behavior.
type ServerConfig struct {
	ListenAddr          string
	DataDir             string
	StatePath           string
	APIToken            string
	GitHubToken         string
	GitHubWebhookSecret string
	BotLogin            string
	RunnerMode          string
	RunnerImage         string
	CodexAuthPath       string
	MaxRuns             int
}

// ClientConfig controls rascal CLI behavior.
type ClientConfig struct {
	ServerURL   string
	APIToken    string
	DefaultRepo string
}

func LoadServerConfig() ServerConfig {
	dataDir := envOrDefault("RASCAL_DATA_DIR", "./var/lib/rascal")
	statePath := envOrDefault("RASCAL_STATE_PATH", filepath.Join(dataDir, "state.json"))

	return ServerConfig{
		ListenAddr:          envOrDefault("RASCAL_LISTEN_ADDR", ":8080"),
		DataDir:             dataDir,
		StatePath:           statePath,
		APIToken:            strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")),
		GitHubToken:         strings.TrimSpace(os.Getenv("RASCAL_GITHUB_TOKEN")),
		GitHubWebhookSecret: strings.TrimSpace(os.Getenv("RASCAL_GITHUB_WEBHOOK_SECRET")),
		BotLogin:            strings.TrimSpace(os.Getenv("RASCAL_BOT_LOGIN")),
		RunnerMode:          envOrDefault("RASCAL_RUNNER_MODE", "noop"),
		RunnerImage:         envOrDefault("RASCAL_RUNNER_IMAGE", "rascal-runner:latest"),
		CodexAuthPath:       envOrDefault("RASCAL_CODEX_AUTH_PATH", "/etc/rascal/codex_auth.json"),
		MaxRuns:             200,
	}
}

func (c ServerConfig) Ensure() error {
	if c.DataDir == "" {
		return fmt.Errorf("data directory cannot be empty")
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(c.DataDir, "runs"), 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	return nil
}

func (c ServerConfig) AuthEnabled() bool {
	return c.APIToken != ""
}

func DefaultClientConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("RASCAL_CONFIG_PATH")); v != "" {
		return v
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return ".rascal/config.yaml"
	}
	return filepath.Join(home, ".rascal", "config.yaml")
}

func LoadClientConfig() ClientConfig {
	cfg := ClientConfig{ServerURL: "http://127.0.0.1:8080"}

	path := DefaultClientConfigPath()
	data, err := os.ReadFile(path)
	if err == nil {
		mergeClient(&cfg, parseSimpleYAML(data))
	}

	fromEnv := ClientConfig{
		ServerURL:   strings.TrimSpace(os.Getenv("RASCAL_SERVER_URL")),
		APIToken:    strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")),
		DefaultRepo: strings.TrimSpace(os.Getenv("RASCAL_DEFAULT_REPO")),
	}
	mergeClient(&cfg, fromEnv)

	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.ServerURL == "" {
		cfg.ServerURL = "http://127.0.0.1:8080"
	}
	return cfg
}

func SaveClientConfig(path string, cfg ClientConfig) error {
	if path == "" {
		path = DefaultClientConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	content := fmt.Sprintf("server_url: %s\napi_token: %s\ndefault_repo: %s\n", cfg.ServerURL, cfg.APIToken, cfg.DefaultRepo)
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		return fmt.Errorf("write client config: %w", err)
	}
	return nil
}

func parseSimpleYAML(data []byte) ClientConfig {
	var out ClientConfig
	s := bufio.NewScanner(bytes.NewReader(data))
	for s.Scan() {
		line := strings.TrimSpace(s.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, `"'`)
		switch key {
		case "server_url":
			out.ServerURL = val
		case "api_token":
			out.APIToken = val
		case "default_repo":
			out.DefaultRepo = val
		}
	}
	return out
}

func mergeClient(dst *ClientConfig, src ClientConfig) {
	if strings.TrimSpace(src.ServerURL) != "" {
		dst.ServerURL = strings.TrimSpace(src.ServerURL)
	}
	if strings.TrimSpace(src.APIToken) != "" {
		dst.APIToken = strings.TrimSpace(src.APIToken)
	}
	if strings.TrimSpace(src.DefaultRepo) != "" {
		dst.DefaultRepo = strings.TrimSpace(src.DefaultRepo)
	}
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
