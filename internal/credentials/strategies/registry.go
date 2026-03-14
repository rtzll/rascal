package strategies

import (
	"fmt"

	"github.com/rtzll/rascal/internal/credentials"
	"github.com/rtzll/rascal/internal/credentialstrategy"
)

func ByName(name credentialstrategy.Name) (credentials.AllocationStrategy, error) {
	normalized, err := credentialstrategy.ParseName(name.String())
	if err != nil {
		return nil, fmt.Errorf("parse credential strategy %q: %w", name, err)
	}
	switch normalized {
	case credentialstrategy.NameRequesterOwnThenShared:
		return RequesterOwnThenShared{}, nil
	case credentialstrategy.NameSharedLeastActiveLeases:
		return SharedLeastActiveLeases{}, nil
	case credentialstrategy.NameSharedWeightedUsage:
		return SharedWeightedUsage{}, nil
	case credentialstrategy.NamePriorityBurst:
		return PriorityBurst{}, nil
	case credentialstrategy.NameHybridReservePlusPool:
		return HybridReservePlusPool{}, nil
	default:
		return nil, fmt.Errorf("unknown credential strategy %q", name)
	}
}
