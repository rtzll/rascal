package credentialstrategy

import "testing"

func TestNormalizeName(t *testing.T) {
	t.Parallel()

	if got := NormalizeName(""); got != DefaultName {
		t.Fatalf("NormalizeName(\"\") = %q, want %q", got, DefaultName)
	}
	if got := NormalizeName(" PRIORITY_BURST "); got != NamePriorityBurst {
		t.Fatalf("NormalizeName(priority_burst) = %q, want %q", got, NamePriorityBurst)
	}
}

func TestParseName(t *testing.T) {
	t.Parallel()

	got, err := ParseName("shared_weighted_usage")
	if err != nil {
		t.Fatalf("ParseName returned error: %v", err)
	}
	if got != NameSharedWeightedUsage {
		t.Fatalf("ParseName = %q, want %q", got, NameSharedWeightedUsage)
	}

	if _, err := ParseName("unknown"); err == nil {
		t.Fatal("expected error for unknown strategy")
	}
}
