package strategies

import "github.com/rtzll/rascal/internal/credentials"

type PriorityBurst struct{}

func (PriorityBurst) Name() string { return "priority_burst" }

func (PriorityBurst) Select(_ credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSortByID(candidates)
	bestID := ""
	bestSpare := -1
	bestShared := false
	for _, candidate := range sorted {
		if !hasCapacity(candidate) {
			continue
		}
		spare := candidate.MaxActiveLeases - candidate.ActiveLeases
		isShared := candidate.Scope == "shared"
		if spare > bestSpare || (spare == bestSpare && isShared && !bestShared) {
			bestID = candidate.ID
			bestSpare = spare
			bestShared = isShared
		}
	}
	if bestID == "" {
		return "", credentials.ErrNoCredentialMatch
	}
	return bestID, nil
}
