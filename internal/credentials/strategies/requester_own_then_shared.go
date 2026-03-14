package strategies

import (
	"cmp"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type RequesterOwnThenShared struct{}

func (RequesterOwnThenShared) Name() string { return "requester_own_then_shared" }

func (RequesterOwnThenShared) Select(req credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSort(candidates, func(a, b credentials.CredentialState) int {
		return cmp.Compare(a.ID, b.ID)
	})
	own := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopePersonal && candidate.OwnerUserID == req.UserID && hasCapacity(candidate)
	})
	if best, ok := minBy(own, func(a, b credentials.CredentialState) bool {
		return a.ActiveLeases < b.ActiveLeases
	}); ok {
		return best.ID, nil
	}
	shared := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if best, ok := minBy(shared, func(a, b credentials.CredentialState) bool {
		return a.ActiveLeases < b.ActiveLeases
	}); ok {
		return best.ID, nil
	}
	return "", credentials.ErrNoCredentialMatch
}
