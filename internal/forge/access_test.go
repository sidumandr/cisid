package forge

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// tokenClient records the token it reaches the forge with.
type tokenClient struct {
	fakeClient
	token string
	used  *string
}

func (c *tokenClient) WithToken(token string) Client {
	return &tokenClient{token: token, used: c.used}
}

func (c *tokenClient) Repository(_ context.Context, path string) (Repository, error) {
	*c.used = c.token
	return Repository{CloneURL: "https://example.com/" + path, DefaultBranch: "main"}, nil
}

func (c *tokenClient) CloneCredentials() (string, string) { return "x-access-token", c.token }

func (c *tokenClient) Whoami(context.Context) (string, error) {
	if c.token == "bad" {
		return "", ErrNotFound
	}
	return "login-of-" + c.token, nil
}

func TestSetReachesEachRepositoryWithItsToken(t *testing.T) {
	used := ""
	s := NewSet()
	s.Add(GitHub, &tokenClient{token: "server-token", used: &used})
	s.Add(Gitea, &fakeClient{})
	s.UseCredentials(func(_ context.Context, key string) (string, bool) {
		return "ilhans-token", key == "sidumandr/biopotan"
	})
	ctx := t.Context()

	_, err := s.Repository(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.Equal(t, "ilhans-token", used, "a repository connected with a user's access uses it")

	_, err = s.Repository(ctx, "sidumandr/cisid")
	require.NoError(t, err)
	assert.Equal(t, "server-token", used, "others use the server's token")

	_, err = s.Repository(WithAccess(ctx, GitHub, "ayses-token"), "sidumandr/cisid")
	require.NoError(t, err)
	assert.Equal(t, "ayses-token", used, "the context's access comes first")

	_, token := s.CloneCredentials(ctx, "sidumandr/biopotan")
	assert.Equal(t, "ilhans-token", token, "the agent clones with it too")

	login, err := s.Whoami(ctx, GitHub, "abc")
	require.NoError(t, err)
	assert.Equal(t, "login-of-abc", login)
	_, err = s.Whoami(ctx, GitHub, "bad")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = s.Whoami(ctx, Gitea, "abc")
	assert.ErrorIs(t, err, ErrNoAccounts, "a client that cannot take a token has no accounts")
	_, err = s.Whoami(ctx, GitLab, "abc")
	assert.ErrorIs(t, err, ErrUnknownForge)

	assert.Equal(t, []string{GitHub}, s.Accounts())
}
