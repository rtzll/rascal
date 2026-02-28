package main

import (
	"testing"
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
