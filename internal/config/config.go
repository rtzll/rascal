package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/agent"
	"github.com/rtzll/rascal/internal/defaults"
	"github.com/spf13/viper"
)

// ServerConfig controls rascald runtime behavior.
type ServerConfig struct {
	ListenAddr              string
	DataDir                 string
	StatePath               string
	Slot                    string
	ActiveSlotPath          string
	APIToken                string
	GitHubToken             string
	GitHubWebhookSecret     string
	BotLogin                string
	RunnerMode              string
	AgentBackend            agent.Backend
	RunnerImage             string
	RunnerImageGoose        string
	RunnerImageCodex        string
	RunnerMaxAttempts       int
	CredentialStrategy      string
	CredentialLeaseTTL      time.Duration
	CredentialRenewEvery    time.Duration
	CredentialEncryptionKey string
	GooseSessionMode        string
	GooseSessionRoot        string
	GooseSessionTTLDays     int
	AgentSessionMode        agent.SessionMode
	AgentSessionRoot        string
	AgentSessionTTLDays     int
	MaxRuns                 int
}

// ClientConfig controls rascal CLI behavior.
type ClientConfig struct {
	ServerURL   string
	APIToken    string
	DefaultRepo string
	Host        string
	Domain      string
	SSHHost     string
	SSHUser     string
	SSHKey      string
	SSHPort     int
}

func LoadServerConfig() ServerConfig {
	dataDir := envOrDefault("RASCAL_DATA_DIR", "./var/lib/rascal")
	statePath := envOrDefault("RASCAL_STATE_PATH", filepath.Join(dataDir, "state.db"))

	cfg := ServerConfig{
		ListenAddr:              envOrDefault("RASCAL_LISTEN_ADDR", ":8080"),
		DataDir:                 dataDir,
		StatePath:               statePath,
		Slot:                    strings.TrimSpace(os.Getenv("RASCAL_SLOT")),
		ActiveSlotPath:          envOrDefault("RASCAL_ACTIVE_SLOT_PATH", "/etc/rascal/active_slot"),
		APIToken:                strings.TrimSpace(os.Getenv("RASCAL_API_TOKEN")),
		GitHubToken:             strings.TrimSpace(os.Getenv("RASCAL_GITHUB_TOKEN")),
		GitHubWebhookSecret:     strings.TrimSpace(os.Getenv("RASCAL_GITHUB_WEBHOOK_SECRET")),
		BotLogin:                strings.TrimSpace(os.Getenv("RASCAL_BOT_LOGIN")),
		RunnerMode:              envOrDefault("RASCAL_RUNNER_MODE", "noop"),
		AgentBackend:            loadAgentBackend(),
		RunnerImageGoose:        envOrDefault("RASCAL_RUNNER_IMAGE_GOOSE", defaults.GooseRunnerImageTag),
		RunnerImageCodex:        envOrDefault("RASCAL_RUNNER_IMAGE_CODEX", defaults.CodexRunnerImageTag),
		RunnerMaxAttempts:       envIntOrDefault("RASCAL_RUNNER_MAX_ATTEMPTS", 1),
		CredentialStrategy:      envOrDefault("RASCAL_CREDENTIAL_STRATEGY", "requester_own_then_shared"),
		CredentialLeaseTTL:      envDurationOrDefault("RASCAL_CREDENTIAL_LEASE_TTL", 90*time.Second),
		CredentialRenewEvery:    envDurationOrDefault("RASCAL_CREDENTIAL_RENEW_INTERVAL", 30*time.Second),
		CredentialEncryptionKey: firstNonEmptyEnv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "RASCAL_API_TOKEN"),
		AgentSessionMode:        loadAgentSessionMode(),
		AgentSessionRoot:        loadAgentSessionRoot(dataDir),
		AgentSessionTTLDays:     loadAgentSessionTTLDays(),
		MaxRuns:                 200,
	}
	cfg.RunnerImage = cfg.RunnerImageForBackend(cfg.AgentBackend)
	cfg.GooseSessionMode = string(cfg.AgentSessionMode)
	cfg.GooseSessionRoot = cfg.AgentSessionRoot
	cfg.GooseSessionTTLDays = cfg.AgentSessionTTLDays
	return cfg
}

func (c ServerConfig) Ensure() error {
	if legacyImage := strings.TrimSpace(os.Getenv("RASCAL_RUNNER_IMAGE")); legacyImage != "" && strings.TrimSpace(os.Getenv("RASCAL_RUNNER_IMAGE_GOOSE")) == "" {
		return fmt.Errorf("RASCAL_RUNNER_IMAGE is no longer accepted without explicit RASCAL_RUNNER_IMAGE_GOOSE")
	}
	if c.DataDir == "" {
		return fmt.Errorf("data directory cannot be empty")
	}
	if err := os.MkdirAll(c.DataDir, 0o755); err != nil {
		return fmt.Errorf("create data directory: %w", err)
	}
	if err := os.MkdirAll(filepath.Join(c.DataDir, "runs"), 0o755); err != nil {
		return fmt.Errorf("create runs directory: %w", err)
	}
	if c.EffectiveAgentSessionMode() != agent.SessionModeOff {
		root := strings.TrimSpace(c.EffectiveAgentSessionRoot())
		if root == "" {
			root = filepath.Join(c.DataDir, defaults.AgentSessionDirName)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return fmt.Errorf("create agent sessions directory: %w", err)
		}
	}
	return nil
}

func (c ServerConfig) RunnerImageForBackend(backend agent.Backend) string {
	switch agent.NormalizeBackend(string(backend)) {
	case agent.BackendCodex:
		return strings.TrimSpace(c.RunnerImageCodex)
	default:
		return strings.TrimSpace(c.RunnerImageGoose)
	}
}

func (c ServerConfig) EffectiveAgentSessionMode() agent.SessionMode {
	if raw := strings.TrimSpace(c.GooseSessionMode); raw != "" && raw != string(c.AgentSessionMode) {
		return agent.NormalizeSessionMode(raw)
	}
	return agent.NormalizeSessionMode(string(c.AgentSessionMode))
}

func (c ServerConfig) EffectiveAgentSessionRoot() string {
	if root := strings.TrimSpace(c.GooseSessionRoot); root != "" && root != strings.TrimSpace(c.AgentSessionRoot) {
		return root
	}
	return strings.TrimSpace(c.AgentSessionRoot)
}

func (c ServerConfig) EffectiveAgentSessionTTLDays() int {
	if c.GooseSessionTTLDays != 0 && c.GooseSessionTTLDays != c.AgentSessionTTLDays {
		return c.GooseSessionTTLDays
	}
	if c.GooseSessionTTLDays == 0 && c.AgentSessionTTLDays != 0 && strings.TrimSpace(c.GooseSessionRoot) != strings.TrimSpace(c.AgentSessionRoot) {
		return 0
	}
	return c.AgentSessionTTLDays
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
		SSHHost:     strings.TrimSpace(v.GetString("ssh_host")),
		SSHUser:     strings.TrimSpace(v.GetString("ssh_user")),
		SSHKey:      strings.TrimSpace(v.GetString("ssh_key")),
		SSHPort:     v.GetInt("ssh_port"),
	}
	cfg.ServerURL = strings.TrimRight(cfg.ServerURL, "/")
	if cfg.ServerURL == "" {
		cfg.ServerURL = "http://127.0.0.1:8080"
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

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
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

func envNonNegativeIntOrDefault(key string, fallback int) int {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	var out int
	if _, err := fmt.Sscanf(v, "%d", &out); err != nil || out < 0 {
		return fallback
	}
	return out
}

func envDurationOrDefault(key string, fallback time.Duration) time.Duration {
	v := strings.TrimSpace(os.Getenv(key))
	if v == "" {
		return fallback
	}
	out, err := time.ParseDuration(v)
	if err != nil || out <= 0 {
		return fallback
	}
	return out
}

func loadAgentBackend() agent.Backend {
	return agent.NormalizeBackend(envOrDefault("RASCAL_AGENT_BACKEND", "codex"))
}

func loadAgentSessionMode() agent.SessionMode {
	if raw, ok := os.LookupEnv("RASCAL_GOOSE_SESSION_MODE"); ok {
		mode := agent.NormalizeSessionMode(raw)
		if mode == "" || mode == agent.SessionModeOff {
			if strings.TrimSpace(raw) == "" {
				return agent.SessionModeAll
			}
		}
		return mode
	}
	if raw, ok := os.LookupEnv("RASCAL_AGENT_SESSION_MODE"); ok {
		mode := agent.NormalizeSessionMode(raw)
		if mode == "" || mode == agent.SessionModeOff {
			if strings.TrimSpace(raw) == "" {
				return agent.SessionModeAll
			}
		}
		return mode
	}
	return agent.SessionModeAll
}

func loadAgentSessionRoot(dataDir string) string {
	if raw, ok := os.LookupEnv("RASCAL_GOOSE_SESSION_ROOT"); ok {
		if strings.TrimSpace(raw) == "" {
			return filepath.Join(dataDir, defaults.AgentSessionDirName)
		}
		return strings.TrimSpace(raw)
	}
	if raw, ok := os.LookupEnv("RASCAL_AGENT_SESSION_ROOT"); ok {
		if strings.TrimSpace(raw) == "" {
			return filepath.Join(dataDir, defaults.AgentSessionDirName)
		}
		return strings.TrimSpace(raw)
	}
	return filepath.Join(dataDir, defaults.AgentSessionDirName)
}

func loadAgentSessionTTLDays() int {
	if _, ok := os.LookupEnv("RASCAL_GOOSE_SESSION_TTL_DAYS"); ok {
		return envNonNegativeIntOrDefault("RASCAL_GOOSE_SESSION_TTL_DAYS", 14)
	}
	if _, ok := os.LookupEnv("RASCAL_AGENT_SESSION_TTL_DAYS"); !ok {
		return 14
	}
	return envNonNegativeIntOrDefault("RASCAL_AGENT_SESSION_TTL_DAYS", 14)
}
