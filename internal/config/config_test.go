package config

import (
	"os"
	"path/filepath"
	"testing"
)

func TestParseSimpleYAML(t *testing.T) {
	t.Parallel()

	cfg := parseSimpleYAML([]byte(`
# comment
server_url: https://rascal.example.com
api_token: abc123
default_repo: owner/repo
`))

	if cfg.ServerURL != "https://rascal.example.com" {
		t.Fatalf("unexpected server url: %s", cfg.ServerURL)
	}
	if cfg.APIToken != "abc123" {
		t.Fatalf("unexpected api token: %s", cfg.APIToken)
	}
	if cfg.DefaultRepo != "owner/repo" {
		t.Fatalf("unexpected default repo: %s", cfg.DefaultRepo)
	}
}

func TestSaveAndLoadClientConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
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

	out := LoadClientConfig()
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
