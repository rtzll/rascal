package strategies

import (
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type PriorityBurst struct{}

func (PriorityBurst) Name() string { return "priority_burst" }

func (PriorityBurst) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSortByID(candidates)
	bestID := ""
	bestLoad := 0
	bestShared := false
	for _, candidate := range sorted {
		isShared := candidate.Scope == state.CredentialScopeShared
		if bestID == "" || candidate.ActiveLeases < bestLoad || (candidate.ActiveLeases == bestLoad && isShared && !bestShared) {
			bestID = candidate.ID
			bestLoad = candidate.ActiveLeases
			bestShared = isShared
		}
	}
	if bestID == "" {
		return "", credentials.ErrNoCredentialMatch
	}
	return bestID, nil
}
