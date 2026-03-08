package strategies

import "github.com/rtzll/rascal/internal/credentials"

type HybridReservePlusPool struct{}

func (HybridReservePlusPool) Name() string { return "hybrid_reserve_plus_pool" }

func (HybridReservePlusPool) Select(req credentials.AcquireRequest, candidates []credentials.CredentialState) (string, error) {
	sorted := cloneAndSortByID(candidates)
	personal := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == "personal" && candidate.OwnerUserID == req.UserID && hasCapacity(candidate)
	})
	for _, candidate := range personal {
		reserve := candidate.MaxActiveLeases / 2
		if reserve < 1 {
			reserve = 1
		}
		if candidate.ActiveLeases < reserve {
			return candidate.ID, nil
		}
	}
	shared := filter(sorted, func(candidate credentials.CredentialState) bool {
		return candidate.Scope == "shared" && hasCapacity(candidate)
	})
	if len(shared) == 0 {
		if len(personal) > 0 {
			return personal[0].ID, nil
		}
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
