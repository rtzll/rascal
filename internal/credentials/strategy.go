package credentials

import (
	"fmt"

	"github.com/rtzll/rascal/internal/credentialstrategy"
)

type AllocationStrategy interface {
	Name() credentialstrategy.Name
	Select(req AcquireRequest, candidates []CredentialState) (credentialID string, err error)
}

var ErrNoCredentialMatch = fmt.Errorf("no credential matched allocation strategy")
