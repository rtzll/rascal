package strategies

import (
	"math"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type SharedWeightedUsage struct{}

func (SharedWeightedUsage) Name() string { return "shared_weighted_usage" }

func (SharedWeightedUsage) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	shared := filter(cloneAndSortByID(candidates), func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if len(shared) == 0 {
		return "", credentials.ErrNoCredentialMatch
	}
	bestID := ""
	bestScore := math.MaxFloat64
	for _, candidate := range shared {
		weight := float64(candidate.Weight)
		if weight < 1 {
			weight = 1
		}
		load := float64(candidate.ActiveLeases) / weight
		usagePenalty := float64(candidate.UsageTokens)/weight/1_000_000 + float64(candidate.UsageRuns)/weight
		score := load + usagePenalty
		if score < bestScore {
			bestID = candidate.ID
			bestScore = score
		}
	}
	if bestID == "" {
		return "", credentials.ErrNoCredentialMatch
	}
	return bestID, nil
}
