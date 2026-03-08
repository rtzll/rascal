package main

import (
	"strings"
	"testing"

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
	t.Cleanup(func() {
		runRemoteDoctorFn = origRunRemoteDoctor
		deployToExistingHostFn = origDeploy
		remoteCaddyDomainConfiguredFn = origCaddy
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
			CodexAuthPresent:   true,
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

	a := &app{}
	result, err := a.runDeployExisting(deployExistingInput{
		Host:           "203.0.113.10",
		SSHPort:        22,
		GOARCH:         "amd64",
		Domain:         "rascal.example.com",
		SkipEnvUpload:  true,
		SkipAuthUpload: true,
		SkipIfHealthy:  true,
		RawErrors:      true,
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
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
	})

	deployHost := ""
	deployToExistingHostFn = func(cfg deployConfig) error {
		deployHost = cfg.Host
		return nil
	}

	a := &app{
		cfg: config.ClientConfig{
			Host: "203.0.113.10",
		},
	}
	result, err := a.runDeployExisting(deployExistingInput{
		SSHPort:        22,
		GOARCH:         "amd64",
		SkipEnvUpload:  true,
		SkipAuthUpload: true,
		RawErrors:      true,
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
	t.Cleanup(func() {
		deployToExistingHostFn = origDeploy
	})

	deployToExistingHostFn = func(cfg deployConfig) error {
		return nil
	}

	t.Setenv("RASCAL_GITHUB_TOKEN", "runtime-token")

	a := &app{}
	_, err := a.runDeployExisting(deployExistingInput{
		Host:           "203.0.113.10",
		SSHPort:        22,
		GOARCH:         "amd64",
		SkipAuthUpload: true,
		RawErrors:      true,
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
		Host:           "203.0.113.10",
		SSHPort:        22,
		GOARCH:         "amd64",
		SkipAuthUpload: true,
		RawErrors:      true,
	})
	if err == nil {
		t.Fatal("expected error")
	}
	if !strings.Contains(err.Error(), "--github-runtime-token is required when --upload-env is used") {
		t.Fatalf("unexpected error: %v", err)
	}
}
