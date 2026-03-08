package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/rtzll/rascal/internal/config"
)

func TestDefaultHetznerFirewallRules(t *testing.T) {
	rules := defaultHetznerFirewallRules()
	if len(rules) != 3 {
		t.Fatalf("expected 3 firewall rules, got %d", len(rules))
	}
	for _, r := range rules {
		if r.Port == nil || *r.Port == "" {
			t.Fatalf("rule missing port: %+v", r)
		}
		if len(r.SourceIPs) != 2 {
			t.Fatalf("expected dual-stack source IPs, got %d", len(r.SourceIPs))
		}
	}
}

func TestNormalizeAuthorizedPublicKey(t *testing.T) {
	const keyType = "ssh-ed25519"
	const keyMaterial = "AAAAC3NzaC1lZDI1NTE5AAAAIE3mTz6L0/KQ42hK0sG9wUEIPAd5T3sGx2tMKN95sF1x"

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "basic key with comment",
			in:   keyType + " " + keyMaterial + " user@host",
			want: keyType + " " + keyMaterial,
		},
		{
			name: "options before key",
			in:   `command="echo hi",no-port-forwarding ` + keyType + " " + keyMaterial + " user@host",
			want: keyType + " " + keyMaterial,
		},
		{
			name: "invalid",
			in:   "not-a-key",
			want: "",
		},
	}

	for _, tt := range tests {
		if got := normalizeAuthorizedPublicKey(tt.in); got != tt.want {
			t.Fatalf("%s: normalizeAuthorizedPublicKey(%q) = %q, want %q", tt.name, tt.in, got, tt.want)
		}
	}
}

func TestRunDeployExistingSkipsHealthyHost(t *testing.T) {
	origRunRemoteDoctor := runRemoteDoctorFn
	origDeploy := deployToExistingHostFn
	origCaddy := remoteCaddyDomainConfiguredFn
	origHealth := waitForServerHealthFn
	origSeed := seedBootstrapSharedCredentialFn
	t.Cleanup(func() {
		runRemoteDoctorFn = origRunRemoteDoctor
		deployToExistingHostFn = origDeploy
		remoteCaddyDomainConfiguredFn = origCaddy
		waitForServerHealthFn = origHealth
		seedBootstrapSharedCredentialFn = origSeed
	})

	runRemoteDoctorFn = func(cfg deployConfig) (remoteDoctorStatus, error) {
		return remoteDoctorStatus{
			Host:               cfg.Host,
			RascalService:      true,
			DockerInstalled:    true,
			SQLiteInstalled:    true,
			CaddyInstalled:     true,
			EnvFilePresent:     true,
			AuthRuntimeSynced:  true,
			RunnerImagePresent: true,
		}, nil
	}
	remoteCaddyDomainConfiguredFn = func(cfg deployConfig, domain string) (bool, error) {
		return true, nil
	}
	deployCalled := false
	deployToExistingHostFn = func(cfg deployConfig) error {
		deployCalled = true
		return nil
	}

	waitForServerHealthFn = func(baseURL string, timeout time.Duration) error { return nil }
	seedBootstrapSharedCredentialFn = func(client apiClient, authFilePath string) (credentialRecord, error) {
		return credentialRecord{}, nil
	}

	a := &app{}
	result, err := a.runDeployExisting(deployExistingInput{
		Host:          "203.0.113.10",
		SSHPort:       22,
		GOARCH:        "amd64",
		Domain:        "rascal.example.com",
		SkipEnvUpload: true,
		SkipIfHealthy: true,
		RawErrors:     true,
	})
	if err != nil {
		t.Fatalf("runDeployExisting failed: %v", err)
	}
	if deployCalled {
		t.Fatal("expected deploy to be skipped for healthy host")
	}
	if result.DeployPerformed {
		t.Fatal("expected DeployPerformed=false for healthy host")
	}
}

func TestRunDeployExistingUsesConfiguredHost(t *testing.T) {
	origDeploy := deployToExistingHostFn
	origHealth := waitForServerHealthFn
	origSeed := seedBootstrapSharedCredentialFn
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
		waitForServerHealthFn = origHealth
		seedBootstrapSharedCredentialFn = origSeed
	})

	deployHost := ""
	deployToExistingHostFn = func(cfg deployConfig) error {
		deployHost = cfg.Host
		return nil
	}
	waitForServerHealthFn = func(baseURL string, timeout time.Duration) error { return nil }
	seedBootstrapSharedCredentialFn = func(client apiClient, authFilePath string) (credentialRecord, error) {
		return credentialRecord{}, nil
	}

	a := &app{
		cfg: config.ClientConfig{
			Host: "203.0.113.10",
		},
	}
	result, err := a.runDeployExisting(deployExistingInput{
		SSHPort:       22,
		GOARCH:        "amd64",
		SkipEnvUpload: true,
		RawErrors:     true,
	})
	if err != nil {
		t.Fatalf("runDeployExisting failed: %v", err)
	}
	if deployHost != "203.0.113.10" {
		t.Fatalf("deploy host = %q, want config host", deployHost)
	}
	if result.Host != "203.0.113.10" {
		t.Fatalf("result host = %q, want config host", result.Host)
	}
}

func TestRunDeployExistingUsesCanonicalRuntimeTokenEnv(t *testing.T) {
	origDeploy := deployToExistingHostFn
	origHealth := waitForServerHealthFn
	origSeed := seedBootstrapSharedCredentialFn
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
		waitForServerHealthFn = origHealth
		seedBootstrapSharedCredentialFn = origSeed
	})

	deployToExistingHostFn = func(cfg deployConfig) error {
		return nil
	}
	waitForServerHealthFn = func(baseURL string, timeout time.Duration) error { return nil }
	seedBootstrapSharedCredentialFn = func(client apiClient, authFilePath string) (credentialRecord, error) {
		return credentialRecord{}, nil
	}

	t.Setenv("RASCAL_GITHUB_TOKEN", "runtime-token")

	a := &app{}
	_, err := a.runDeployExisting(deployExistingInput{
		Host:      "203.0.113.10",
		SSHPort:   22,
		GOARCH:    "amd64",
		RawErrors: true,
	})
	if err != nil {
		t.Fatalf("runDeployExisting failed: %v", err)
	}
}

func TestRunDeployExistingIgnoresLegacyRuntimeTokenEnv(t *testing.T) {
	t.Setenv("RASCAL_GITHUB_TOKEN", "")
	t.Setenv("GITHUB_RUNTIME_TOKEN", "legacy-runtime-token")

	a := &app{}
	_, err := a.runDeployExisting(deployExistingInput{
		Host:      "203.0.113.10",
		SSHPort:   22,
		GOARCH:    "amd64",
		RawErrors: true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--github-runtime-token is required when --upload-env is used") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRunDeployExistingSeedsStoredCredentialWhenCodexAuthProvided(t *testing.T) {
	origDeploy := deployToExistingHostFn
	origHealth := waitForServerHealthFn
	origSeed := seedBootstrapSharedCredentialFn
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
		waitForServerHealthFn = origHealth
		seedBootstrapSharedCredentialFn = origSeed
	})

	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	deployCalled := false
	deployToExistingHostFn = func(cfg deployConfig) error {
		deployCalled = true
		return nil
	}
	healthCalls := 0
	waitForServerHealthFn = func(baseURL string, timeout time.Duration) error {
		healthCalls++
		return nil
	}
	var gotClient apiClient
	var gotAuthPath string
	seedBootstrapSharedCredentialFn = func(client apiClient, authFilePath string) (credentialRecord, error) {
		gotClient = client
		gotAuthPath = authFilePath
		return credentialRecord{ID: bootstrapSharedCredentialID}, nil
	}

	a := &app{cfg: config.ClientConfig{APIToken: "cfg-api-token"}}
	_, err := a.runDeployExisting(deployExistingInput{
		Host:          "203.0.113.10",
		SSHUser:       "root",
		SSHPort:       22,
		GOARCH:        "amd64",
		SkipEnvUpload: true,
		CodexAuthPath: authPath,
		RawErrors:     true,
	})
	if err != nil {
		t.Fatalf("runDeployExisting failed: %v", err)
	}
	if !deployCalled {
		t.Fatal("expected deploy to run")
	}
	if healthCalls != 1 {
		t.Fatalf("health checks = %d, want 1", healthCalls)
	}
	if gotAuthPath != authPath {
		t.Fatalf("seed auth path = %q, want %q", gotAuthPath, authPath)
	}
	if gotClient.baseURL != "http://203.0.113.10:8080" || gotClient.token != "cfg-api-token" {
		t.Fatalf("unexpected seed client: %+v", gotClient)
	}
}

func TestRunDeployExistingRequiresAPITokenForCredentialSeeding(t *testing.T) {
	authPath := filepath.Join(t.TempDir(), "auth.json")
	if err := os.WriteFile(authPath, []byte(`{"token":"abc"}`), 0o600); err != nil {
		t.Fatalf("write auth file: %v", err)
	}

	a := &app{}
	_, err := a.runDeployExisting(deployExistingInput{
		Host:          "203.0.113.10",
		SSHPort:       22,
		GOARCH:        "amd64",
		SkipEnvUpload: true,
		CodexAuthPath: authPath,
		RawErrors:     true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--codex-auth requires API access") {
		t.Fatalf("unexpected error: %v", err)
	}
}
