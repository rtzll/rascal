package credentialstrategy

import (
	"fmt"
	"strings"
)

type Name string

const (
	NameRequesterOwnThenShared  Name = "requester_own_then_shared"
	NameSharedLeastActiveLeases Name = "shared_least_active_leases"
	NameSharedWeightedUsage     Name = "shared_weighted_usage"
	NamePriorityBurst           Name = "priority_burst"
	NameHybridReservePlusPool   Name = "hybrid_reserve_plus_pool"
	DefaultName                      = NameRequesterOwnThenShared
)

func NormalizeName(raw string) Name {
	normalized := strings.ToLower(strings.TrimSpace(raw))
	if normalized == "" {
		return DefaultName
	}
	return Name(normalized)
}

func (n Name) String() string {
	return string(n)
}

func (n Name) IsValid() bool {
	switch NormalizeName(n.String()) {
	case NameRequesterOwnThenShared,
		NameSharedLeastActiveLeases,
		NameSharedWeightedUsage,
		NamePriorityBurst,
		NameHybridReservePlusPool:
		return true
	default:
		return false
	}
}

func ParseName(raw string) (Name, error) {
	name := NormalizeName(raw)
	if !name.IsValid() {
		return "", fmt.Errorf("unknown credential strategy %q", raw)
	}
	return name, nil
}
