package strategies

import (
	"sort"

	"github.com/rtzll/rascal/internal/credentials"
)

func cloneAndSortByID(candidates []credentials.CredentialState) []credentials.CredentialState {
	out := append([]credentials.CredentialState(nil), candidates...)
	sort.Slice(out, func(i, j int) bool {
		return out[i].ID < out[j].ID
	})
	return out
}

func filter(candidates []credentials.CredentialState, pred func(credentials.CredentialState) bool) []credentials.CredentialState {
	out := make([]credentials.CredentialState, 0, len(candidates))
	for _, candidate := range candidates {
		if pred(candidate) {
			out = append(out, candidate)
		}
	}
	return out
}

func hasCapacity(candidate credentials.CredentialState) bool {
	return true
}
