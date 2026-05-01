package remote

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSSHArgsDefaultsAndKeyExpansion(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	args := SSHArgs(SSHConfig{
		Host:    "rascal.example.com",
		KeyPath: "~/.ssh/id_ed25519",
	}, "echo ok")

	want := []string{
		"-p", "22",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=10",
		"-i", filepath.Join(os.Getenv("HOME"), ".ssh", "id_ed25519"),
		"root@rascal.example.com",
		"echo ok",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("SSHArgs() = %v, want %v", args, want)
	}
}

func TestSCPArgsUsesExplicitPortAndKey(t *testing.T) {
	t.Setenv("HOME", t.TempDir())

	args := SCPArgs(SSHConfig{
		Host:    "rascal.example.com",
		User:    "deploy",
		KeyPath: "~/.ssh/deploy_key",
		Port:    2222,
	}, "local.txt", RemoteTarget(SSHConfig{Host: "rascal.example.com", User: "deploy"}, "/tmp/remote.txt"))

	want := []string{
		"-P", "2222",
		"-o", "BatchMode=yes",
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "ServerAliveInterval=30",
		"-o", "ServerAliveCountMax=10",
		"-i", filepath.Join(os.Getenv("HOME"), ".ssh", "deploy_key"),
		"local.txt",
		"deploy@rascal.example.com:/tmp/remote.txt",
	}
	if !reflect.DeepEqual(args, want) {
		t.Fatalf("SCPArgs() = %v, want %v", args, want)
	}
}

func TestRemoteTargetDefaultsUser(t *testing.T) {
	got := RemoteTarget(SSHConfig{Host: "rascal.example.com"}, "/srv/rascal")
	want := "root@rascal.example.com:/srv/rascal"
	if got != want {
		t.Fatalf("RemoteTarget() = %q, want %q", got, want)
	}
}

func TestScriptNormalizesTrailingNewline(t *testing.T) {
	got := Script("set -eu", "echo ok", "")
	want := "set -eu\necho ok\n"
	if got != want {
		t.Fatalf("Script() = %q, want %q", got, want)
	}

	if Script("", "   ") != "" {
		t.Fatalf("Script() should return empty string for blank input")
	}
}

func TestShellQuoteEscapesSingleQuotes(t *testing.T) {
	got := ShellQuote("it's ready")
	want := `'it'"'"'s ready'`
	if got != want {
		t.Fatalf("ShellQuote() = %q, want %q", got, want)
	}
}

func TestExpandPathHandlesHomeAndPlainPaths(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	got, err := ExpandPath("~/keys/id_ed25519")
	if err != nil {
		t.Fatalf("ExpandPath() error = %v", err)
	}
	want := filepath.Join(home, "keys", "id_ed25519")
	if got != want {
		t.Fatalf("ExpandPath() = %q, want %q", got, want)
	}

	got, err = ExpandPath("/tmp/key")
	if err != nil {
		t.Fatalf("ExpandPath() plain path error = %v", err)
	}
	if got != "/tmp/key" {
		t.Fatalf("ExpandPath() plain path = %q, want %q", got, "/tmp/key")
	}
}
