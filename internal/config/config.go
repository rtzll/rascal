package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
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
	RunnerMaxAttempts   int
	CodexAuthPath       string
	MaxRuns             int
}

// ClientConfig controls rascal CLI behavior.
type ClientConfig struct {
	ServerURL   string
	APIToken    string
	DefaultRepo string
	Host        string
	Domain      string
	Transport   string
	SSHHost     string
	SSHUser     string
	SSHKey      string
	SSHPort     int
}

func LoadServerConfig() ServerConfig {
	dataDir := envOrDefault("RASCAL_DATA_DIR", "./var/lib/rascal")
	statePath := envOrDefault("RASCAL_STATE_PATH", filepath.Join(dataDir, "state.db"))

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
		RunnerMaxAttempts:   envIntOrDefault("RASCAL_RUNNER_MAX_ATTEMPTS", 1),
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
		return ".rascal/config.toml"
	}
	return filepath.Join(home, ".rascal", "config.toml")
}

func LoadClientConfig() ClientConfig {
	cfg, err := LoadClientConfigAtPath(DefaultClientConfigPath())
	if err != nil {
		return ClientConfig{ServerURL: "http://127.0.0.1:8080"}
	}
	return cfg
}

func LoadClientConfigAtPath(path string) (ClientConfig, error) {
	v := viper.New()
	if strings.TrimSpace(path) == "" {
		path = DefaultClientConfigPath()
	}
	v.SetConfigFile(path)
	v.SetConfigType("toml")
	v.SetDefault("server_url", "http://127.0.0.1:8080")
	v.SetEnvPrefix("RASCAL")
	v.SetEnvKeyReplacer(strings.NewReplacer(".", "_"))
	v.AutomaticEnv()

	if err := v.ReadInConfig(); err != nil {
		var notFound viper.ConfigFileNotFoundError
		if !errors.As(err, &notFound) && !os.IsNotExist(err) {
			return ClientConfig{}, fmt.Errorf("read client config: %w", err)
		}
	}

	cfg := ClientConfig{
		ServerURL:   strings.TrimSpace(v.GetString("server_url")),
		APIToken:    strings.TrimSpace(v.GetString("api_token")),
		DefaultRepo: strings.TrimSpace(v.GetString("default_repo")),
		Host:        strings.TrimSpace(v.GetString("host")),
		Domain:      strings.TrimSpace(v.GetString("domain")),
		Transport:   strings.TrimSpace(v.GetString("transport")),
		SSHHost:     strings.TrimSpace(v.GetString("ssh_host")),
		SSHUser:     strings.TrimSpace(v.GetString("ssh_user")),
		SSHKey:      strings.TrimSpace(v.GetString("ssh_key")),
		SSHPort:     v.GetInt("ssh_port"),
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.ServerURL == "" {
		cfg.ServerURL = "http://127.0.0.1:8080"
	}
	if cfg.Transport == "" {
		cfg.Transport = "auto"
	}
	if cfg.SSHUser == "" {
		cfg.SSHUser = "root"
	}
	if cfg.SSHPort <= 0 {
		cfg.SSHPort = 22
	}
	return cfg, nil
}

func SaveClientConfig(path string, cfg ClientConfig) error {
	if path == "" {
		path = DefaultClientConfigPath()
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}

	v := viper.New()
	v.SetConfigType("toml")
	v.Set("server_url", strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/"))
	v.Set("api_token", strings.TrimSpace(cfg.APIToken))
	v.Set("default_repo", strings.TrimSpace(cfg.DefaultRepo))
	v.Set("host", strings.TrimSpace(cfg.Host))
	v.Set("domain", strings.TrimSpace(cfg.Domain))
	v.Set("transport", strings.TrimSpace(cfg.Transport))
	v.Set("ssh_host", strings.TrimSpace(cfg.SSHHost))
	v.Set("ssh_user", strings.TrimSpace(cfg.SSHUser))
	v.Set("ssh_key", strings.TrimSpace(cfg.SSHKey))
	v.Set("ssh_port", cfg.SSHPort)

	if err := v.WriteConfigAs(path); err != nil {
		return fmt.Errorf("write client config: %w", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		return fmt.Errorf("chmod client config: %w", err)
	}
	return nil
}

func envOrDefault(key, fallback string) string {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	return v
}

func envIntOrDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil || out <= 0 {
		return fallback
	}
	return out
}
