package strategies

import (
	"cmp"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/state"
)

type HybridReservePlusPool struct{}

func (HybridReservePlusPool) Name() string { return "hybrid_reserve_plus_pool" }

func (HybridReservePlusPool) Select(req credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSort(candidates, func(a, b credentials.CredentialState) int {
		return cmp.Compare(a.ID, b.ID)
	})
	personal := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopePersonal && candidate.OwnerUserID == req.UserID && hasCapacity(candidate)
	})
	if available, ok := first(personal, func(candidate credentials.CredentialState) bool {
		return candidate.ActiveLeases == 0
	}); ok {
		return available.ID, nil
	}
	shared := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == state.CredentialScopeShared && hasCapacity(candidate)
	})
	if best, ok := minBy(shared, func(a, b credentials.CredentialState) bool {
		return a.ActiveLeases < b.ActiveLeases
	}); ok {
		return best.ID, nil
	}
	if len(personal) > 0 {
		return personal[0].ID, nil
	}
	return "", credentials.ErrNoCredentialMatch
}
