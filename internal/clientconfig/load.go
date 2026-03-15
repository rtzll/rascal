package clientconfig

import (
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/pelletier/go-toml/v2"
	"github.com/rtzll/rascal/internal/config"
)

type File struct {
	ServerURL   *string `toml:"server_url,omitempty"`
	APIToken    *string `toml:"api_token,omitempty"`
	DefaultRepo *string `toml:"default_repo,omitempty"`
	Host        *string `toml:"host,omitempty"`
	Domain      *string `toml:"domain,omitempty"`
	Transport   *string `toml:"transport,omitempty"`
	SSHHost     *string `toml:"ssh_host,omitempty"`
	SSHUser     *string `toml:"ssh_user,omitempty"`
	SSHKey      *string `toml:"ssh_key,omitempty"`
	SSHPort     *int    `toml:"ssh_port,omitempty"`
}

func Load(path string) (config.ClientConfig, error) {
	cfg, err := config.LoadClientConfigAtPath(path)
	if err != nil {
		return config.ClientConfig{}, fmt.Errorf("load client config at %s: %w", path, err)
	}
	return cfg, nil
}

func LoadFile(path string) (File, bool, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return File{}, false, nil
		}
		return File{}, false, fmt.Errorf("read config file: %w", err)
	}
	var settings File
	if err := toml.Unmarshal(data, &settings); err != nil {
		return File{}, false, fmt.Errorf("decode config file: %w", err)
	}
	return settings, true, nil
}

type ResolveInput struct {
	Path            string
	ServerURLFlag   string
	APITokenFlag    string
	DefaultRepoFlag string
	TransportFlag   string
	SSHHostFlag     string
	SSHUserFlag     string
	SSHKeyFlag      string
	SSHPortFlag     int
}

type ResolveResult struct {
	Config            config.ClientConfig
	ServerSource      string
	TokenSource       string
	RepoSource        string
	TransportSource   string
	ResolvedTransport string
}

func Resolve(input ResolveInput, resolveTransport func(configured, serverURL, sshHost string) string) (ResolveResult, error) {
	cfg, err := Load(input.Path)
	if err != nil {
		return ResolveResult{}, err
	}
	fileSettings, exists, err := LoadFile(input.Path)
	if err != nil {
		return ResolveResult{}, err
	}
	_ = exists

	result := ResolveResult{Config: cfg}
	result.ServerSource = sourceForString(input.ServerURLFlag, "RASCAL_SERVER_URL", fileSettings.ServerURL, "default")
	result.TokenSource = sourceForString(input.APITokenFlag, "RASCAL_API_TOKEN", fileSettings.APIToken, "unset")
	result.RepoSource = sourceForString(input.DefaultRepoFlag, "RASCAL_DEFAULT_REPO", fileSettings.DefaultRepo, "unset")
	result.TransportSource = sourceForString(input.TransportFlag, "RASCAL_TRANSPORT", fileSettings.Transport, "default")

	if strings.TrimSpace(input.ServerURLFlag) != "" {
		result.Config.ServerURL = strings.TrimSpace(input.ServerURLFlag)
	}
	if strings.TrimSpace(input.APITokenFlag) != "" {
		result.Config.APIToken = strings.TrimSpace(input.APITokenFlag)
	}
	if strings.TrimSpace(input.DefaultRepoFlag) != "" {
		result.Config.DefaultRepo = strings.TrimSpace(input.DefaultRepoFlag)
	}
	if strings.TrimSpace(input.TransportFlag) != "" {
		result.Config.Transport = strings.ToLower(strings.TrimSpace(input.TransportFlag))
	}
	if strings.TrimSpace(input.SSHHostFlag) != "" {
		result.Config.SSHHost = strings.TrimSpace(input.SSHHostFlag)
	}
	if strings.TrimSpace(input.SSHUserFlag) != "" {
		result.Config.SSHUser = strings.TrimSpace(input.SSHUserFlag)
	}
	if strings.TrimSpace(input.SSHKeyFlag) != "" {
		result.Config.SSHKey = strings.TrimSpace(input.SSHKeyFlag)
	}
	if input.SSHPortFlag > 0 {
		result.Config.SSHPort = input.SSHPortFlag
	}

	result.ResolvedTransport = resolveTransport(result.Config.Transport, result.Config.ServerURL, result.Config.SSHHost)
	if result.TransportSource == "default" {
		result.TransportSource = "resolved"
	}
	return result, nil
}

func sourceForString(flagValue, envKey string, fileValue *string, fallback string) string {
	if strings.TrimSpace(flagValue) != "" {
		return "flag"
	}
	if strings.TrimSpace(os.Getenv(envKey)) != "" {
		return "env"
	}
	if fileValue != nil {
		return "config"
	}
	return fallback
}
