package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
)

// listingClient is a client that lists repositories, or fails to.
type listingClient struct {
	fakeClient
	paths []ListedPath
	err   error
}

func (l *listingClient) Repositories(context.Context) ([]ListedPath, error) { return l.paths, l.err }

func TestSetListsEveryForgeItCan(t *testing.T) {
	s := NewSet()
	s.Add(GitHub, &listingClient{paths: []ListedPath{{Path: "sidumandr/biopotan", Private: true}, {Path: "not a path"}}})
	s.Add(GitLab, &listingClient{paths: []ListedPath{{Path: "group/sub/app"}}})
	s.Add(Gitea, &listingClient{err: ErrNoListing})
	s.Add(Bitbucket, &fakeClient{})

	l := s.Repositories(t.Context())
	assert.Equal(t, []Listed{
		{Repo: "sidumandr/biopotan", Forge: GitHub, Private: true},
		{Repo: "gitlab:group/sub/app", Forge: GitLab},
	}, l.Repos, "keys as cisid names them, and nothing that is not a repository path")
	assert.Equal(t, []string{Bitbucket, Gitea}, l.Unlisted, "a forge without a token, or that cannot list, is named")
}
