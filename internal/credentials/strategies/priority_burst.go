package strategies

import (
	"cmp"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/credentialstrategy"
	"github.com/rtzll/rascal/internal/state"
)

type PriorityBurst struct{}

func (PriorityBurst) Name() credentialstrategy.Name {
	return credentialstrategy.NamePriorityBurst
}

func (PriorityBurst) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSort(candidates, func(a, b credentials.CredentialState) int {
		return cmp.Compare(a.ID, b.ID)
	})
	if best, ok := minBy(sorted, func(a, b credentials.CredentialState) bool {
		aShared := a.Scope == state.CredentialScopeShared
		bShared := b.Scope == state.CredentialScopeShared
		return a.ActiveLeases < b.ActiveLeases || (a.ActiveLeases == b.ActiveLeases && aShared && !bShared)
	}); ok {
		return best.ID, nil
	}
	return "", credentials.ErrNoCredentialMatch
}
