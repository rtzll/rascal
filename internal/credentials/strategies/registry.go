package strategies

import (
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/credentials"
)

func ByName(name string) (credentials.AllocationStrategy, error) {
	switch strings.TrimSpace(strings.ToLower(name)) {
	case "", "requester_own_then_shared":
		return RequesterOwnThenShared{}, nil
	case "shared_least_active_leases":
		return SharedLeastActiveLeases{}, nil
	case "shared_weighted_usage":
		return SharedWeightedUsage{}, nil
	case "priority_burst":
		return PriorityBurst{}, nil
	case "hybrid_reserve_plus_pool":
		return HybridReservePlusPool{}, nil
	default:
		return nil, fmt.Errorf("unknown credential strategy %q", name)
	}
}
