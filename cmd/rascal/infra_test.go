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
