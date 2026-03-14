package strategies

import (
	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type RequesterOwnThenShared struct{}

func (RequesterOwnThenShared) Name() string { return "requester_own_then_shared" }

func (RequesterOwnThenShared) Select(req credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSortByID(candidates)
	own := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopePersonal && candidate.OwnerUserID == req.UserID && hasCapacity(candidate)
	})
	if len(own) > 0 {
		best := own[0]
		for _, candidate := range own[1:] {
			if candidate.ActiveLeases < best.ActiveLeases {
				best = candidate
			}
		}
		return best.ID, nil
	}
	shared := filter(sorted, func(candidate credentials.CredentialState) bool {
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
