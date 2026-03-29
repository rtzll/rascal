package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/rtzll/rascal/internal/runner"
	"github.com/rtzll/rascal/internal/runtime"
)

func TestSaveAndLoadClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	t.Setenv("RASCAL_CONFIG_PATH", path)
	t.Setenv("RASCAL_SERVER_URL", "")
	t.Setenv("RASCAL_API_TOKEN", "")
	t.Setenv("RASCAL_DEFAULT_REPO", "")

	in := ClientConfig{
		ServerURL:   "https://rascal.example.com",
		APIToken:    "token-xyz",
		DefaultRepo: "owner/repo",
		Host:        "203.0.113.10",
		Domain:      "rascal.example.com",
		Transport:   "ssh",
		SSHHost:     "203.0.113.10",
		SSHUser:     "root",
		SSHKey:      "~/.ssh/id_ed25519",
		SSHPort:     22,
	}
	if err := SaveClientConfig(path, in); err != nil {
		t.Fatalf("save client config: %v", err)
	}

	out, err := LoadClientConfigAtPath(path)
	if err != nil {
		t.Fatalf("load client config: %v", err)
	}
	if out.ServerURL != in.ServerURL {
		t.Fatalf("server url mismatch: got %s want %s", out.ServerURL, in.ServerURL)
	}
	if out.APIToken != in.APIToken {
		t.Fatalf("api token mismatch: got %s want %s", out.APIToken, in.APIToken)
	}
	if out.DefaultRepo != in.DefaultRepo {
		t.Fatalf("default repo mismatch: got %s want %s", out.DefaultRepo, in.DefaultRepo)
	}
	if out.Host != in.Host {
		t.Fatalf("host mismatch: got %s want %s", out.Host, in.Host)
	}
	if out.Domain != in.Domain {
		t.Fatalf("domain mismatch: got %s want %s", out.Domain, in.Domain)
	}
	if out.Transport != in.Transport {
		t.Fatalf("transport mismatch: got %s want %s", out.Transport, in.Transport)
	}
	if out.SSHHost != in.SSHHost {
		t.Fatalf("ssh host mismatch: got %s want %s", out.SSHHost, in.SSHHost)
	}
	if out.SSHUser != in.SSHUser {
		t.Fatalf("ssh user mismatch: got %s want %s", out.SSHUser, in.SSHUser)
	}
	if out.SSHKey != in.SSHKey {
		t.Fatalf("ssh key mismatch: got %s want %s", out.SSHKey, in.SSHKey)
	}
	if out.SSHPort != in.SSHPort {
		t.Fatalf("ssh port mismatch: got %d want %d", out.SSHPort, in.SSHPort)
	}

	st, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat config file: %v", err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("unexpected file mode: %o", st.Mode().Perm())
	}
}

func TestDefaultClientConfigPathUsesExplicitOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "custom.toml")
	t.Setenv("RASCAL_CONFIG_PATH", path)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(t.TempDir(), "xdg"))
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	if got := DefaultClientConfigPath(); got != path {
		t.Fatalf("DefaultClientConfigPath() = %q, want %q", got, path)
	}
}

func TestDefaultClientConfigPathUsesXDGConfigHome(t *testing.T) {
	xdg := filepath.Join(t.TempDir(), "xdg")
	t.Setenv("RASCAL_CONFIG_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", xdg)
	t.Setenv("HOME", filepath.Join(t.TempDir(), "home"))

	want := filepath.Join(xdg, "rascal", "config.toml")
	if got := DefaultClientConfigPath(); got != want {
		t.Fatalf("DefaultClientConfigPath() = %q, want %q", got, want)
	}
}

func TestDefaultClientConfigPathFallsBackToDotConfigInHome(t *testing.T) {
	home := filepath.Join(t.TempDir(), "home")
	t.Setenv("RASCAL_CONFIG_PATH", "")
	t.Setenv("XDG_CONFIG_HOME", "")
	t.Setenv("HOME", home)

	want := filepath.Join(home, ".config", "rascal", "config.toml")
	if got := DefaultClientConfigPath(); got != want {
		t.Fatalf("DefaultClientConfigPath() = %q, want %q", got, want)
	}
}

func TestLoadClientConfigEnvOverride(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("server_url = \"https://rascal.example.com\"\napi_token = \"from_file\"\ndefault_repo = \"owner/repo\"\nhost = \"203.0.113.10\"\ndomain = \"rascal.example.com\"\ntransport = \"ssh\"\nssh_host = \"203.0.113.10\"\nssh_user = \"root\"\nssh_key = \"~/.ssh/id_ed25519\"\nssh_port = 22\n"), 0o600); err != nil {
		t.Fatalf("write config: %v", err)
	}

	t.Setenv("RASCAL_SERVER_URL", "https://from-env.example.com")
	cfg, err := LoadClientConfigAtPath(path)
	if err != nil {
		t.Fatalf("load client config: %v", err)
	}

	if cfg.ServerURL != "https://from-env.example.com" {
		t.Fatalf("expected env override, got %s", cfg.ServerURL)
	}
	if cfg.APIToken != "from_file" {
		t.Fatalf("expected api token from file, got %s", cfg.APIToken)
	}
	if cfg.Host != "203.0.113.10" {
		t.Fatalf("expected host from file, got %s", cfg.Host)
	}
	if cfg.Domain != "rascal.example.com" {
		t.Fatalf("expected domain from file, got %s", cfg.Domain)
	}
	if cfg.Transport != "ssh" {
		t.Fatalf("expected transport from file, got %s", cfg.Transport)
	}
	if cfg.SSHHost != "203.0.113.10" {
		t.Fatalf("expected ssh host from file, got %s", cfg.SSHHost)
	}
	if cfg.SSHUser != "root" {
		t.Fatalf("expected ssh user from file, got %s", cfg.SSHUser)
	}
	if cfg.SSHKey != "~/.ssh/id_ed25519" {
		t.Fatalf("expected ssh key from file, got %s", cfg.SSHKey)
	}
	if cfg.SSHPort != 22 {
		t.Fatalf("expected ssh port from file, got %d", cfg.SSHPort)
	}
}

func TestLoadServerConfigGooseSessionDefaults(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "rascal-data")
	t.Setenv("RASCAL_DATA_DIR", dataDir)

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.TaskSession.Mode != runtime.SessionModeAll {
		t.Fatalf("TaskSession.Mode = %q, want all", cfg.TaskSession.Mode)
	}
	wantRoot := filepath.Join(dataDir, "agent-sessions")
	if cfg.TaskSession.Root != wantRoot {
		t.Fatalf("TaskSession.Root = %q, want %q", cfg.TaskSession.Root, wantRoot)
	}
	if cfg.TaskSession.TTLDays != 14 {
		t.Fatalf("TaskSession.TTLDays = %d, want 14", cfg.TaskSession.TTLDays)
	}
}

func TestLoadServerConfigDefaultsAgentRuntimeToGoose(t *testing.T) {
	t.Setenv("RASCAL_AGENT_RUNTIME", "")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.AgentRuntime != "goose-codex" {
		t.Fatalf("AgentRuntime = %q, want goose-codex", cfg.AgentRuntime)
	}
	if cfg.RunnerMode != runner.ModeNoop {
		t.Fatalf("RunnerMode = %q, want noop", cfg.RunnerMode)
	}
	if cfg.RunnerImage != "rascal-runner-goose-codex:latest" {
		t.Fatalf("RunnerImage = %q, want rascal-runner-goose-codex:latest", cfg.RunnerImage)
	}
}

func TestLoadServerConfigGooseSessionOverrides(t *testing.T) {
	root := filepath.Join(t.TempDir(), "goose-root")
	t.Setenv("RASCAL_TASK_SESSION_MODE", "PR-ONLY")
	t.Setenv("RASCAL_TASK_SESSION_ROOT", root)
	t.Setenv("RASCAL_TASK_SESSION_TTL_DAYS", "0")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.TaskSession.Mode != runtime.SessionModePROnly {
		t.Fatalf("TaskSession.Mode = %q, want pr-only", cfg.TaskSession.Mode)
	}
	if cfg.TaskSession.Root != root {
		t.Fatalf("TaskSession.Root = %q, want %q", cfg.TaskSession.Root, root)
	}
	if cfg.TaskSession.TTLDays != 0 {
		t.Fatalf("TaskSession.TTLDays = %d, want 0", cfg.TaskSession.TTLDays)
	}
}

func TestLoadServerConfigNormalizesRunnerMode(t *testing.T) {
	t.Setenv("RASCAL_RUNNER_MODE", "DOCKER")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.RunnerMode != runner.ModeDocker {
		t.Fatalf("RunnerMode = %q, want docker", cfg.RunnerMode)
	}
}

func TestLoadServerConfigDefaultsDockerSecurityToBaseline(t *testing.T) {
	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.RunnerSecurity.Mode != runner.DockerSecurityBaseline {
		t.Fatalf("RunnerSecurity.Mode = %q, want baseline", cfg.RunnerSecurity.Mode)
	}
	if cfg.RunnerSecurity.CPUs != "2" {
		t.Fatalf("RunnerSecurity.CPUs = %q, want 2", cfg.RunnerSecurity.CPUs)
	}
	if cfg.RunnerSecurity.Memory != "4g" {
		t.Fatalf("RunnerSecurity.Memory = %q, want 4g", cfg.RunnerSecurity.Memory)
	}
	if cfg.RunnerSecurity.PidsLimit != 256 {
		t.Fatalf("RunnerSecurity.PidsLimit = %d, want 256", cfg.RunnerSecurity.PidsLimit)
	}
	if cfg.RunnerSecurity.TmpfsTmpSize != "512m" {
		t.Fatalf("RunnerSecurity.TmpfsTmpSize = %q, want 512m", cfg.RunnerSecurity.TmpfsTmpSize)
	}
	if cfg.RunnerSecurity.AllowEnvSecrets {
		t.Fatal("RunnerSecurity.AllowEnvSecrets = true, want false")
	}
}

func TestLoadServerConfigDockerSecurityOverrides(t *testing.T) {
	t.Setenv("RASCAL_RUNNER_DOCKER_SECURITY_MODE", "strict")
	t.Setenv("RASCAL_RUNNER_DOCKER_CPUS", "3.5")
	t.Setenv("RASCAL_RUNNER_DOCKER_MEMORY", "6g")
	t.Setenv("RASCAL_RUNNER_DOCKER_PIDS_LIMIT", "384")
	t.Setenv("RASCAL_RUNNER_DOCKER_TMPFS_TMP_SIZE", "768m")
	t.Setenv("RASCAL_RUNNER_ALLOW_ENV_SECRETS", "true")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.RunnerSecurity.Mode != runner.DockerSecurityStrict {
		t.Fatalf("RunnerSecurity.Mode = %q, want strict", cfg.RunnerSecurity.Mode)
	}
	if cfg.RunnerSecurity.CPUs != "3.5" {
		t.Fatalf("RunnerSecurity.CPUs = %q, want 3.5", cfg.RunnerSecurity.CPUs)
	}
	if cfg.RunnerSecurity.Memory != "6g" {
		t.Fatalf("RunnerSecurity.Memory = %q, want 6g", cfg.RunnerSecurity.Memory)
	}
	if cfg.RunnerSecurity.PidsLimit != 384 {
		t.Fatalf("RunnerSecurity.PidsLimit = %d, want 384", cfg.RunnerSecurity.PidsLimit)
	}
	if cfg.RunnerSecurity.TmpfsTmpSize != "768m" {
		t.Fatalf("RunnerSecurity.TmpfsTmpSize = %q, want 768m", cfg.RunnerSecurity.TmpfsTmpSize)
	}
	if !cfg.RunnerSecurity.AllowEnvSecrets {
		t.Fatal("RunnerSecurity.AllowEnvSecrets = false, want true")
	}
}

func TestLoadServerConfigCredentialEncryptionKeyFallback(t *testing.T) {
	t.Setenv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "")
	t.Setenv("RASCAL_API_TOKEN", "api-token-fallback")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.CredentialEncryptionKey != "api-token-fallback" {
		t.Fatalf("CredentialEncryptionKey = %q, want api-token-fallback", cfg.CredentialEncryptionKey)
	}

	t.Setenv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "explicit-key")
	cfg, err = LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if cfg.CredentialEncryptionKey != "explicit-key" {
		t.Fatalf("CredentialEncryptionKey = %q, want explicit-key", cfg.CredentialEncryptionKey)
	}
}

func TestLoadServerConfigRejectsInvalidEnumEnv(t *testing.T) {
	tests := []struct {
		name   string
		key    string
		value  string
		needle string
	}{
		{name: "runner mode", key: "RASCAL_RUNNER_MODE", value: "podman", needle: "unknown runner mode"},
		{name: "docker security mode", key: "RASCAL_RUNNER_DOCKER_SECURITY_MODE", value: "paranoid", needle: "unknown docker security mode"},
		{name: "agent runtime", key: "RASCAL_AGENT_RUNTIME", value: "unknown-agent", needle: "unknown agent runtime"},
		{name: "credential strategy", key: "RASCAL_CREDENTIAL_STRATEGY", value: "weighted", needle: "unknown credential strategy"},
		{name: "task session mode", key: "RASCAL_TASK_SESSION_MODE", value: "sometimes", needle: "unknown agent session mode"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(tt.key, tt.value)
			_, err := LoadServerConfig()
			if err == nil {
				t.Fatalf("LoadServerConfig error = nil, want error")
			}
			if !strings.Contains(err.Error(), tt.needle) {
				t.Fatalf("LoadServerConfig error = %q, want substring %q", err.Error(), tt.needle)
			}
		})
	}
}

func TestServerConfigEnsureRejectsMissingAPIToken(t *testing.T) {
	t.Setenv("RASCAL_DATA_DIR", filepath.Join(t.TempDir(), "rascal-data"))
	t.Setenv("RASCAL_API_TOKEN", "")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if err := cfg.Ensure(); err == nil {
		t.Fatal("Ensure error = nil, want missing token failure")
	} else if !strings.Contains(err.Error(), "RASCAL_API_TOKEN is required") {
		t.Fatalf("Ensure error = %q, want missing token message", err.Error())
	}
}

func TestServerConfigEnsureAllowsPresentAPIToken(t *testing.T) {
	t.Setenv("RASCAL_DATA_DIR", filepath.Join(t.TempDir(), "rascal-data"))
	t.Setenv("RASCAL_API_TOKEN", "test-token")

	cfg, err := LoadServerConfig()
	if err != nil {
		t.Fatalf("LoadServerConfig returned error: %v", err)
	}
	if err := cfg.Ensure(); err != nil {
		t.Fatalf("Ensure returned error: %v", err)
	}
}
