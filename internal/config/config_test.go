package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
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
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "")
	t.Setenv("RASCAL_GOOSE_SESSION_ROOT", "")
	t.Setenv("RASCAL_GOOSE_SESSION_TTL_DAYS", "")

	cfg := LoadServerConfig()
	if cfg.GooseSessionMode != "all" {
		t.Fatalf("GooseSessionMode = %q, want all", cfg.GooseSessionMode)
	}
	wantRoot := filepath.Join(dataDir, "agent-sessions")
	if cfg.GooseSessionRoot != wantRoot {
		t.Fatalf("GooseSessionRoot = %q, want %q", cfg.GooseSessionRoot, wantRoot)
	}
	if cfg.GooseSessionTTLDays != 14 {
		t.Fatalf("GooseSessionTTLDays = %d, want 14", cfg.GooseSessionTTLDays)
	}
}

func TestLoadServerConfigDefaultsAgentBackendToCodex(t *testing.T) {
	t.Setenv("RASCAL_AGENT_BACKEND", "")

	cfg := LoadServerConfig()
	if cfg.AgentBackend != "codex" {
		t.Fatalf("AgentBackend = %q, want codex", cfg.AgentBackend)
	}
	if cfg.RunnerImage != "rascal-runner-codex:latest" {
		t.Fatalf("RunnerImage = %q, want rascal-runner-codex:latest", cfg.RunnerImage)
	}
}

func TestLoadServerConfigGooseSessionOverrides(t *testing.T) {
	root := filepath.Join(t.TempDir(), "goose-root")
	t.Setenv("RASCAL_GOOSE_SESSION_MODE", "PR-ONLY")
	t.Setenv("RASCAL_GOOSE_SESSION_ROOT", root)
	t.Setenv("RASCAL_GOOSE_SESSION_TTL_DAYS", "0")

	cfg := LoadServerConfig()
	if cfg.GooseSessionMode != "pr-only" {
		t.Fatalf("GooseSessionMode = %q, want pr-only", cfg.GooseSessionMode)
	}
	if cfg.GooseSessionRoot != root {
		t.Fatalf("GooseSessionRoot = %q, want %q", cfg.GooseSessionRoot, root)
	}
	if cfg.GooseSessionTTLDays != 0 {
		t.Fatalf("GooseSessionTTLDays = %d, want 0", cfg.GooseSessionTTLDays)
	}
}

func TestLoadServerConfigCredentialEncryptionKeyFallback(t *testing.T) {
	t.Setenv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "")
	t.Setenv("RASCAL_API_TOKEN", "api-token-fallback")

	cfg := LoadServerConfig()
	if cfg.CredentialEncryptionKey != "api-token-fallback" {
		t.Fatalf("CredentialEncryptionKey = %q, want api-token-fallback", cfg.CredentialEncryptionKey)
	}

	t.Setenv("RASCAL_CREDENTIAL_ENCRYPTION_KEY", "explicit-key")
	cfg = LoadServerConfig()
	if cfg.CredentialEncryptionKey != "explicit-key" {
		t.Fatalf("CredentialEncryptionKey = %q, want explicit-key", cfg.CredentialEncryptionKey)
	}
}

func TestServerConfigEnsureRejectsLegacyRunnerImageWithoutExplicitGooseImage(t *testing.T) {
	dataDir := filepath.Join(t.TempDir(), "rascal-data")
	t.Setenv("RASCAL_DATA_DIR", dataDir)
	t.Setenv("RASCAL_RUNNER_IMAGE", "rascal-runner:latest")
	t.Setenv("RASCAL_RUNNER_IMAGE_GOOSE", "")

	cfg := LoadServerConfig()
	err := cfg.Ensure()
	if err == nil {
		t.Fatal("expected Ensure to reject legacy runner image env")
	}
	if !strings.Contains(err.Error(), "RASCAL_RUNNER_IMAGE_GOOSE") || !strings.Contains(err.Error(), "RASCAL_RUNNER_IMAGE_CODEX") {
		t.Fatalf("unexpected error: %v", err)
	}
}
