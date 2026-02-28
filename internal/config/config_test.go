package config

import (
	"os"
	"path/filepath"
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
	if err := os.WriteFile(path, []byte("server_url = \"https://rascal.example.com\"\napi_token = \"from_file\"\ndefault_repo = \"owner/repo\"\n"), 0o600); err != nil {
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
}
