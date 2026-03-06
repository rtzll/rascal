package runner

import (
	"context"
	"testing"
)

func TestNormalizeEgressMode(t *testing.T) {
	t.Parallel()

	cases := map[string]string{
		"":             egressModeOpen,
		"  ":           egressModeOpen,
		"OPEN":         egressModeOpen,
		"safe-default": egressModeSafeDefault,
		"AllowList":    egressModeAllowlist,
	}
	for in, want := range cases {
		if got := normalizeEgressMode(in); got != want {
			t.Fatalf("normalizeEgressMode(%q)=%q want %q", in, got, want)
		}
	}
}

func TestResolveAllowlistCIDRAndIP(t *testing.T) {
	t.Parallel()

	allow, err := resolveAllowlist(context.Background(), []string{
		"10.1.2.0/24",
		"192.0.2.10",
		"2001:db8::/32",
		"2001:db8::5",
	})
	if err != nil {
		t.Fatalf("resolveAllowlist: %v", err)
	}
	if len(allow.IPv4) != 2 {
		t.Fatalf("expected 2 IPv4 prefixes, got %d (%v)", len(allow.IPv4), allow.IPv4)
	}
	if len(allow.IPv6) != 2 {
		t.Fatalf("expected 2 IPv6 prefixes, got %d (%v)", len(allow.IPv6), allow.IPv6)
	}
}

func TestResolveAllowlistInvalid(t *testing.T) {
	t.Parallel()

	if _, err := resolveAllowlist(context.Background(), []string{"$%not-a-target%$"}); err == nil {
		t.Fatal("expected invalid allowlist entry error")
	}
}
