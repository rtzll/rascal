package strategies

import (
	"cmp"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state"
)

type SharedLeastActiveLeases struct{}

func (SharedLeastActiveLeases) Name() credentialstrategy.Name {
	return credentialstrategy.NameSharedLeastActiveLeases
}

func (SharedLeastActiveLeases) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	shared := filter(cloneAndSort(candidates, func(a, b credentials.CredentialState) int {
		return cmp.Compare(a.ID, b.ID)
	}), func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if best, ok := minBy(shared, func(a, b credentials.CredentialState) bool {
		return a.ActiveLeases < b.ActiveLeases
	}); ok {
		return best.ID, nil
	}
	return "", credentials.ErrNoCredentialMatch
}
