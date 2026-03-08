package credentials

import "fmt"

type AllocationStrategy interface {
	Name() string
	Select(req AcquireRequest, candidates []CredentialState) (credentialID string, err error)
}

var ErrNoCredentialMatch = fmt.Errorf("no credential matched allocation strategy")
