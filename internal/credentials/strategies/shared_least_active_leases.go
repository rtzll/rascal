package strategies

import (
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type SharedLeastActiveLeases struct{}

func (SharedLeastActiveLeases) Name() string { return "shared_least_active_leases" }

func (SharedLeastActiveLeases) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	shared := filter(cloneAndSortByID(candidates), func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if len(shared) == 0 {
		return "", credentials.ErrNoCredentialMatch
	}
	best := shared[0]
	for _, candidate := range shared[1:] {
		if candidate.ActiveLeases < best.ActiveLeases {
			best = candidate
		}
	}
	return best.ID, nil
}
