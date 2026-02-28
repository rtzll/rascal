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
		Host:        "203.0.113.10",
		Domain:      "rascal.example.com",
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
	if err := os.WriteFile(path, []byte("server_url = \"https://rascal.example.com\"\napi_token = \"from_file\"\ndefault_repo = \"owner/repo\"\nhost = \"203.0.113.10\"\ndomain = \"rascal.example.com\"\n"), 0o600); err != nil {
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
}
