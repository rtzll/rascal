package github

import (
	"fmt"
	"strings"

	"github.com/rtzll/rascal/internal/repositories"
)

type ClientResolver interface {
	ForRepo(fullName string) (*APIClient, error)
}

type PerRepoClientResolver struct {
	repoResolver repositories.Resolver
}

func NewClientResolver(repoResolver repositories.Resolver) *PerRepoClientResolver {
	return &PerRepoClientResolver{repoResolver: repoResolver}
}

func (r *PerRepoClientResolver) ForRepo(fullName string) (*APIClient, error) {
	if r == nil || r.repoResolver == nil {
		return nil, fmt.Errorf("repository client resolver is not configured")
	}
	cfg, err := r.repoResolver.Resolve(strings.TrimSpace(fullName))
	if err != nil {
		return nil, fmt.Errorf("resolve repository %q for github client: %w", strings.TrimSpace(fullName), err)
	}
	return NewAPIClient(cfg.GitHubToken), nil
}
