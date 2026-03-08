package strategies

import (
	"testing"

	"github.com/rtzll/rascal/internal/credentials"
)

func TestRequesterOwnThenSharedSelectsPersonalFirst(t *testing.T) {
	strategy := RequesterOwnThenShared{}
	got, err := strategy.Select(credentials.AcquireRequest{UserID: "u1"}, []credentials.CredentialState{
		{ID: "shared-a", Scope: "shared", ActiveLeases: 0},
		{ID: "personal-a", Scope: "personal", OwnerUserID: "u1", ActiveLeases: 0},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "personal-a" {
		t.Fatalf("credential_id = %s, want personal-a", got)
	}
}

func TestSharedLeastActiveLeases(t *testing.T) {
	strategy := SharedLeastActiveLeases{}
	got, err := strategy.Select(credentials.AcquireRequest{}, []credentials.CredentialState{
		{ID: "shared-a", Scope: "shared", ActiveLeases: 1},
		{ID: "shared-b", Scope: "shared", ActiveLeases: 0},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "shared-b" {
		t.Fatalf("credential_id = %s, want shared-b", got)
	}
}

func TestSharedWeightedUsagePrefersLowerUsageScore(t *testing.T) {
	strategy := SharedWeightedUsage{}
	got, err := strategy.Select(credentials.AcquireRequest{}, []credentials.CredentialState{
		{ID: "shared-a", Scope: "shared", Weight: 1, ActiveLeases: 0, UsageRuns: 9, UsageTokens: 9_000_000},
		{ID: "shared-b", Scope: "shared", Weight: 3, ActiveLeases: 0, UsageRuns: 1, UsageTokens: 500_000},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "shared-b" {
		t.Fatalf("credential_id = %s, want shared-b", got)
	}
}

func TestPriorityBurstPrefersLeastLoadedSharedCredential(t *testing.T) {
	strategy := PriorityBurst{}
	got, err := strategy.Select(credentials.AcquireRequest{}, []credentials.CredentialState{
		{ID: "personal-a", Scope: "personal", ActiveLeases: 2},
		{ID: "shared-a", Scope: "shared", ActiveLeases: 0},
		{ID: "shared-b", Scope: "shared", ActiveLeases: 0},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "shared-a" {
		t.Fatalf("credential_id = %s, want shared-a (lowest load with shared priority)", got)
	}
}

func TestHybridReservePlusPoolUsesSharedAfterReserve(t *testing.T) {
	strategy := HybridReservePlusPool{}
	got, err := strategy.Select(credentials.AcquireRequest{UserID: "u1"}, []credentials.CredentialState{
		{ID: "personal-a", Scope: "personal", OwnerUserID: "u1", ActiveLeases: 3},
		{ID: "shared-a", Scope: "shared", ActiveLeases: 0},
	})
	if err != nil {
		t.Fatalf("select: %v", err)
	}
	if got != "shared-a" {
		t.Fatalf("credential_id = %s, want shared-a", got)
	}
}
