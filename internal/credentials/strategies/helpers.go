package strategies

import (
	"slices"

	"github.com/rtzll/rascal/internal/credentials"
)

func cloneAndSort[T any](values []T, compare func(a, b T) int) []T {
	out := slices.Clone(values)
	slices.SortFunc(out, compare)
	return out
}

func filter[T any](values []T, pred func(T) bool) []T {
	out := make([]T, 0, len(values))
	for _, value := range values {
		if pred(value) {
			out = append(out, value)
		}
	}
	return out
}

func first[T any](values []T, pred func(T) bool) (T, bool) {
	for _, value := range values {
		if pred(value) {
			return value, true
		}
	}
	var zero T
	return zero, false
}

func minBy[T any](values []T, less func(a, b T) bool) (T, bool) {
	if len(values) == 0 {
		var zero T
		return zero, false
	}
	best := values[0]
	for _, value := range values[1:] {
		if less(value, best) {
			best = value
		}
	}
	return best, true
}

func hasCapacity(candidate credentials.CredentialState) bool {
	return true
}
