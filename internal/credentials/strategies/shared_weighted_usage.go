package strategies

import (
	"cmp"
	"math"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type SharedWeightedUsage struct{}

func (SharedWeightedUsage) Name() string { return "shared_weighted_usage" }

func (SharedWeightedUsage) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	shared := filter(cloneAndSort(candidates, func(a, b credentials.CredentialState) int {
		return cmp.Compare(a.ID, b.ID)
	}), func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if best, ok := minBy(shared, func(a, b credentials.CredentialState) bool {
		return weightedUsageScore(a) < weightedUsageScore(b)
	}); ok {
		return best.ID, nil
	}
	return "", credentials.ErrNoCredentialMatch
}

func weightedUsageScore(candidate credentials.CredentialState) float64 {
	weight := math.Max(float64(candidate.Weight), 1)
	load := float64(candidate.ActiveLeases) / weight
	usagePenalty := float64(candidate.UsageTokens)/weight/1_000_000 + float64(candidate.UsageRuns)/weight
	return load + usagePenalty
}
