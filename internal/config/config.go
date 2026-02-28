package config

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// ServerConfig controls rascald runtime behavior.
type ServerConfig struct {
	ListenAddr string
	DataDir    string
	StatePath  string
	APIToken   string
	MaxRuns    int
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
		ListenAddr: envOrDefault("RASCAL_LISTEN_ADDR", ":8080"),
		DataDir:    dataDir,
		StatePath:  statePath,
		APIToken:   strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")),
		MaxRuns:    200,
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

func LoadClientConfig() ClientConfig {
	serverURL := strings.TrimSpace(os.Getenv("RASCAL_SERVER_URL"))
	if serverURL == "" {
		serverURL = "http://127.0.0.1:8080"
	}

	return ClientConfig{
		ServerURL:   strings.TrimRight(serverURL, "/"),
		APIToken:    strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")),
		DefaultRepo: strings.TrimSpace(os.Getenv("RASCAL_DEFAULT_REPO")),
	}
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}
