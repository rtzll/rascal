package clientconfig

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLoadFileMissingReturnsFalse(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing.toml")

	settings, exists, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if exists {
		t.Fatalf("LoadFile() exists = true, want false")
	}
	if settings != (File{}) {
		t.Fatalf("LoadFile() settings = %#v, want zero value", settings)
	}
}

func TestSaveFileRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "rascal", "config.toml")
	serverURL := "https://rascal.example.com"
	token := "token-123"
	repo := "rtzll/rascal"
	transport := "ssh"
	sshHost := "rascal-server"
	sshUser := "deploy"
	sshKey := "~/.ssh/id_ed25519"
	sshPort := 2200

	want := File{
		ServerURL:   &serverURL,
		APIToken:    &token,
		DefaultRepo: &repo,
		Transport:   &transport,
		SSHHost:     &sshHost,
		SSHUser:     &sshUser,
		SSHKey:      &sshKey,
		SSHPort:     &sshPort,
	}
	if err := SaveFile(path, want); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}

	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if perms := info.Mode().Perm(); perms != 0o600 {
		t.Fatalf("config perms = %o, want 600", perms)
	}

	got, exists, err := LoadFile(path)
	if err != nil {
		t.Fatalf("LoadFile() error = %v", err)
	}
	if !exists {
		t.Fatalf("LoadFile() exists = false, want true")
	}
	if derefString(got.ServerURL) != serverURL || derefString(got.APIToken) != token || derefString(got.DefaultRepo) != repo {
		t.Fatalf("LoadFile() round-trip mismatch: %#v", got)
	}
	if derefString(got.Transport) != transport || derefString(got.SSHHost) != sshHost || derefString(got.SSHUser) != sshUser || derefString(got.SSHKey) != sshKey {
		t.Fatalf("LoadFile() ssh round-trip mismatch: %#v", got)
	}
	if derefInt(got.SSHPort) != sshPort {
		t.Fatalf("LoadFile() ssh port = %d, want %d", derefInt(got.SSHPort), sshPort)
	}
}

func TestResolveAppliesSourcesAndFlagOverrides(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	serverURL := "https://config.example.com/"
	token := "config-token"
	repo := "config/repo"
	transport := "auto"
	sshHost := "config-host"
	sshUser := "config-user"
	sshKey := "~/.ssh/config"
	sshPort := 2222
	if err := SaveFile(path, File{
		ServerURL:   &serverURL,
		APIToken:    &token,
		DefaultRepo: &repo,
		Transport:   &transport,
		SSHHost:     &sshHost,
		SSHUser:     &sshUser,
		SSHKey:      &sshKey,
		SSHPort:     &sshPort,
	}); err != nil {
		t.Fatalf("SaveFile() error = %v", err)
	}

	t.Setenv("RASCAL_API_TOKEN", "env-token")

	var resolveArgs []string
	result, err := Resolve(ResolveInput{
		Path:            path,
		ServerURLFlag:   " https://flag.example.com/ ",
		DefaultRepoFlag: " flag/repo ",
		SSHHostFlag:     " flag-host ",
		SSHUserFlag:     " flag-user ",
		SSHKeyFlag:      " ~/.ssh/flag ",
		SSHPortFlag:     2300,
	}, func(configured, serverURL, sshHost string) string {
		resolveArgs = []string{configured, serverURL, sshHost}
		return "ssh"
	})
	if err != nil {
		t.Fatalf("Resolve() error = %v", err)
	}

	if result.ServerSource != "flag" {
		t.Fatalf("ServerSource = %q, want flag", result.ServerSource)
	}
	if result.TokenSource != "env" {
		t.Fatalf("TokenSource = %q, want env", result.TokenSource)
	}
	if result.RepoSource != "flag" {
		t.Fatalf("RepoSource = %q, want flag", result.RepoSource)
	}
	if result.TransportSource != "config" {
		t.Fatalf("TransportSource = %q, want config", result.TransportSource)
	}
	if result.ResolvedTransport != "ssh" {
		t.Fatalf("ResolvedTransport = %q, want ssh", result.ResolvedTransport)
	}
	if got := result.Config.ServerURL; got != "https://flag.example.com/" {
		t.Fatalf("ServerURL = %q, want trimmed flag value", got)
	}
	if got := result.Config.APIToken; got != "env-token" {
		t.Fatalf("APIToken = %q, want env override", got)
	}
	if got := result.Config.DefaultRepo; got != "flag/repo" {
		t.Fatalf("DefaultRepo = %q, want trimmed flag value", got)
	}
	if got := result.Config.SSHHost; got != "flag-host" {
		t.Fatalf("SSHHost = %q, want flag-host", got)
	}
	if got := result.Config.SSHUser; got != "flag-user" {
		t.Fatalf("SSHUser = %q, want flag-user", got)
	}
	if got := result.Config.SSHKey; got != "~/.ssh/flag" {
		t.Fatalf("SSHKey = %q, want trimmed flag value", got)
	}
	if got := result.Config.SSHPort; got != 2300 {
		t.Fatalf("SSHPort = %d, want 2300", got)
	}
	if len(resolveArgs) != 3 || resolveArgs[0] != "auto" || resolveArgs[1] != "https://flag.example.com/" || resolveArgs[2] != "flag-host" {
		t.Fatalf("resolveTransport args = %#v, want [auto https://flag.example.com/ flag-host]", resolveArgs)
	}
}

func derefString(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

func derefInt(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}
