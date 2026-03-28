package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/defaults"
	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtime"
	"github.com/spf13/viper"
)

type TaskSessionConfig struct {
	Mode    runtime.SessionMode
	Root    string
	TTLDays int
}

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
	RunnerMode              runner.Mode
	AgentRuntime            runtime.Runtime
	RunnerImage             string
	RunnerImageGooseCodex   string
	RunnerImageCodex        string
	RunnerImagePi           string
	RunnerImageClaude       string
	RunnerImageGooseClaude  string
	RunnerMaxAttempts       int
	CredentialStrategy      credentialstrategy.Name
	CredentialLeaseTTL      time.Duration
	CredentialRenewEvery    time.Duration
	CredentialEncryptionKey string
	TaskSession             TaskSessionConfig
	MaxRuns                 int
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

func LoadServerConfig() (ServerConfig, error) {
	dataDir := envOrDefault("RASCAL_DATA_DIR", "./var/lib/rascal")
	statePath := envOrDefault("RASCAL_STATE_PATH", filepath.Join(dataDir, "state.db"))
	runnerMode, err := runner.ParseMode(envOrDefault("RASCAL_RUNNER_MODE", string(runner.ModeNoop)))
	if err != nil {
		return ServerConfig{}, fmt.Errorf("parse RASCAL_RUNNER_MODE: %w", err)
	}
	agentRuntime, err := loadAgentRuntime()
	if err != nil {
		return ServerConfig{}, err
	}
	credentialStrategy, err := credentialstrategy.ParseName(envOrDefault("RASCAL_CREDENTIAL_STRATEGY", credentialstrategy.DefaultName.String()))
	if err != nil {
		return ServerConfig{}, fmt.Errorf("parse RASCAL_CREDENTIAL_STRATEGY: %w", err)
	}
	taskSession, err := loadTaskSessionConfig(dataDir)
	if err != nil {
		return ServerConfig{}, err
	}

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
		RunnerMode:              runnerMode,
		AgentRuntime:            agentRuntime,
		RunnerImageGooseCodex:   firstNonEmptyEnvOrDefault(defaults.GooseCodexRunnerImageTag, "RASCAL_RUNNER_IMAGE_GOOSE_CODEX", "RASCAL_RUNNER_IMAGE_GOOSE"),
		RunnerImageCodex:        envOrDefault("RASCAL_RUNNER_IMAGE_CODEX", defaults.CodexRunnerImageTag),
		RunnerImagePi:           envOrDefault("RASCAL_RUNNER_IMAGE_PI", defaults.PiRunnerImageTag),
		RunnerImageClaude:       envOrDefault("RASCAL_RUNNER_IMAGE_CLAUDE", defaults.ClaudeRunnerImageTag),
		RunnerImageGooseClaude:  envOrDefault("RASCAL_RUNNER_IMAGE_GOOSE_CLAUDE", defaults.GooseClaudeRunnerImageTag),
		RunnerMaxAttempts:       envIntOrDefault("RASCAL_RUNNER_MAX_ATTEMPTS", 1),
		CredentialStrategy:      credentialStrategy,
		CredentialLeaseTTL:      envDurationOrDefault("RASCAL_CREDENTIAL_LEASE_TTL", 90*time.Second),
		CredentialRenewEvery:    envDurationOrDefault("RASCAL_CREDENTIAL_RENEW_INTERVAL", 30*time.Second),
		CredentialEncryptionKey: firstNonEmptyEnv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "RASCAL_API_TOKEN"),
		TaskSession:             taskSession,
		MaxRuns:                 200,
	}
	cfg.RunnerImage = cfg.RunnerImageForRuntime(cfg.AgentRuntime)
	return cfg, nil
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
	if c.EffectiveTaskSessionMode() != runtime.SessionModeOff {
		root := strings.TrimSpace(c.EffectiveTaskSessionRoot())
		if root == "" {
			root = filepath.Join(c.DataDir, defaults.AgentSessionDirName)
		}
		if err := os.MkdirAll(root, 0o755); err != nil {
			return fmt.Errorf("create agent sessions directory: %w", err)
		}
	}
	return nil
}

func (c ServerConfig) RunnerImageForRuntime(rt runtime.Runtime) string {
	switch runtime.NormalizeRuntime(string(rt)) {
	case runtime.RuntimeCodex:
		return strings.TrimSpace(c.RunnerImageCodex)
	case runtime.RuntimePi:
		return strings.TrimSpace(c.RunnerImagePi)
	case runtime.RuntimeClaude:
		return strings.TrimSpace(c.RunnerImageClaude)
	case runtime.RuntimeGooseClaude:
		return strings.TrimSpace(c.RunnerImageGooseClaude)
	default:
		return strings.TrimSpace(c.RunnerImageGooseCodex)
	}
}

func (c ServerConfig) EffectiveTaskSessionMode() runtime.SessionMode {
	return runtime.NormalizeSessionMode(string(c.TaskSession.Mode))
}

func (c ServerConfig) EffectiveTaskSessionRoot() string {
	return strings.TrimSpace(c.TaskSession.Root)
}

func (c ServerConfig) EffectiveTaskSessionTTLDays() int {
	return c.TaskSession.TTLDays
}

func (c ServerConfig) AuthEnabled() bool {
	return c.APIToken != ""
}

func DefaultClientConfigPath() string {
	if v := strings.TrimSpace(os.Getenv("RASCAL_CONFIG_PATH")); v != "" {
		return v
	}
	if xdg := strings.TrimSpace(os.Getenv("XDG_CONFIG_HOME")); xdg != "" {
		return filepath.Join(xdg, "rascal", "config.toml")
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return filepath.Join(".config", "rascal", "config.toml")
	}
	return filepath.Join(home, ".config", "rascal", "config.toml")
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

func firstNonEmptyEnv(keys ...string) string {
	for _, key := range keys {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyEnvOrDefault(fallback string, keys ...string) string {
	if v := firstNonEmptyEnv(keys...); v != "" {
		return v
	}
	return fallback
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

func loadAgentRuntime() (runtime.Runtime, error) {
	raw := strings.TrimSpace(os.Getenv("RASCAL_AGENT_RUNTIME"))
	if raw == "" {
		raw = "goose"
	}
	runtime, err := runtime.ParseRuntime(raw)
	if err != nil {
		return "", fmt.Errorf("parse RASCAL_AGENT_RUNTIME: %w", err)
	}
	return runtime, nil
}

func loadTaskSessionConfig(dataDir string) (TaskSessionConfig, error) {
	mode, err := loadTaskSessionMode()
	if err != nil {
		return TaskSessionConfig{}, err
	}
	return TaskSessionConfig{
		Mode:    mode,
		Root:    loadTaskSessionRoot(dataDir),
		TTLDays: loadTaskSessionTTLDays(),
	}, nil
}

func loadTaskSessionMode() (runtime.SessionMode, error) {
	if raw, ok := os.LookupEnv("RASCAL_TASK_SESSION_MODE"); ok {
		if strings.TrimSpace(raw) != "" {
			mode, err := runtime.ParseSessionMode(raw)
			if err != nil {
				return "", fmt.Errorf("parse RASCAL_TASK_SESSION_MODE: %w", err)
			}
			return mode, nil
		}
	}
	return runtime.SessionModeAll, nil
}

func loadTaskSessionRoot(dataDir string) string {
	if raw, ok := os.LookupEnv("RASCAL_TASK_SESSION_ROOT"); ok {
		if strings.TrimSpace(raw) == "" {
			return filepath.Join(dataDir, defaults.AgentSessionDirName)
		}
		return strings.TrimSpace(raw)
	}
	return filepath.Join(dataDir, defaults.AgentSessionDirName)
}

func loadTaskSessionTTLDays() int {
	if _, ok := os.LookupEnv("RASCAL_TASK_SESSION_TTL_DAYS"); ok {
		return envNonNegativeIntOrDefault("RASCAL_TASK_SESSION_TTL_DAYS", 14)
	}
	return 14
}
