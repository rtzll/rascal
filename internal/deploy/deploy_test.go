package deploy

import (
	"strings"
	"testing"
)

func TestGoarchFromUnameMachine(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "x86_64", want: "amd64", ok: true},
		{in: "amd64", want: "amd64", ok: true},
		{in: "aarch64", want: "arm64", ok: true},
		{in: "arm64", want: "arm64", ok: true},
		{in: "ppc64le", want: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := GoarchFromUnameMachine(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("GoarchFromUnameMachine(%q) = (%q, %t), want (%q, %t)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestGoarchFromHetznerArchitecture(t *testing.T) {
	tests := []struct {
		in   string
		want string
		ok   bool
	}{
		{in: "x86", want: "amd64", ok: true},
		{in: "x86_64", want: "amd64", ok: true},
		{in: "amd64", want: "amd64", ok: true},
		{in: "arm", want: "arm64", ok: true},
		{in: "aarch64", want: "arm64", ok: true},
		{in: "arm64", want: "arm64", ok: true},
		{in: "unknown", want: "", ok: false},
	}
	for _, tc := range tests {
		got, ok := GoarchFromHetznerArchitecture(tc.in)
		if got != tc.want || ok != tc.ok {
			t.Fatalf("GoarchFromHetznerArchitecture(%q) = (%q, %t), want (%q, %t)", tc.in, got, ok, tc.want, tc.ok)
		}
	}
}

func TestRenderCaddyfileVariants(t *testing.T) {
	local := renderCaddyfile("")
	if !strings.Contains(local, ":8080 {") {
		t.Fatalf("local caddyfile missing :8080 site:\n%s", local)
	}
	if strings.Contains(local, "example.com {") {
		t.Fatalf("local caddyfile unexpectedly includes domain block:\n%s", local)
	}

	domain := renderCaddyfile("example.com")
	if strings.Contains(domain, ":8080 {") {
		t.Fatalf("domain caddyfile should not include local :8080 block:\n%s", domain)
	}
	if !strings.Contains(domain, "example.com {") {
		t.Fatalf("domain caddyfile missing domain block:\n%s", domain)
	}
}
