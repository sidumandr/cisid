package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreForgeAccounts(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	ilhan, err := s.CreateUser(ctx, models.User{Username: "ilhan", Role: "maintainer"}, "")
	require.NoError(t, err)

	_, err = s.SealedForgeAccount(ctx, ilhan.ID, "github")
	require.ErrorIs(t, err, ErrForgeAccountNotFound)

	expires := time.Now().Add(2 * time.Hour).Truncate(time.Second)
	require.NoError(t, s.PutForgeAccount(ctx, SealedForgeAccount{UserID: ilhan.ID, Forge: "gitlab", Login: "ilhan", Kind: "oauth",
		Token: []byte("sealed-access"), Refresh: []byte("sealed-refresh"), ExpiresAt: &expires}))
	require.NoError(t, s.PutForgeAccount(ctx, SealedForgeAccount{UserID: ilhan.ID, Forge: "github", Login: "old", Kind: "token", Token: []byte("t1")}))
	require.NoError(t, s.PutForgeAccount(ctx, SealedForgeAccount{UserID: ilhan.ID, Forge: "github", Login: "sidumandr", Kind: "token", Token: []byte("t2")}))

	a, err := s.SealedForgeAccount(ctx, ilhan.ID, "github")
	require.NoError(t, err)
	assert.Equal(t, "sidumandr", a.Login, "connecting again replaces it")
	assert.Equal(t, []byte("t2"), a.Token)
	assert.Nil(t, a.Refresh)
	assert.Nil(t, a.ExpiresAt)
	g, err := s.SealedForgeAccount(ctx, ilhan.ID, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, []byte("sealed-refresh"), g.Refresh)
	require.NotNil(t, g.ExpiresAt)
	assert.True(t, expires.Equal(*g.ExpiresAt))

	list, err := s.ForgeAccounts(ctx, ilhan.ID)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, []string{"github", "gitlab"}, []string{list[0].Forge, list[1].Forge})
	assert.Equal(t, "token", list[0].Kind)

	// A repository connected with ilhan's access names him.
	_, err = s.PutRepository(ctx, models.Repository{Repo: "sidumandr/biopotan", CloneURL: "u", DefaultBranch: "main", AddedBy: "ilhan", ViaUserID: &ilhan.ID})
	require.NoError(t, err)
	id, ok, err := s.RepositoryUser(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.True(t, ok)
	assert.Equal(t, ilhan.ID, id)
	_, err = s.PutRepository(ctx, models.Repository{Repo: "sidumandr/biopotan", CloneURL: "u", DefaultBranch: "main", AddedBy: "API token"})
	require.NoError(t, err)
	_, ok, err = s.RepositoryUser(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.True(t, ok, "connecting again without one's own access keeps the access it had")
	repos, err := s.Repositories(ctx)
	require.NoError(t, err)
	require.Len(t, repos, 1)
	assert.Equal(t, "ilhan", repos[0].Via)

	_, ok, err = s.RepositoryUser(ctx, "nobody/connected")
	require.NoError(t, err)
	assert.False(t, ok)

	found, err := s.DeleteForgeAccount(ctx, ilhan.ID, "github")
	require.NoError(t, err)
	assert.True(t, found)
	found, err = s.DeleteForgeAccount(ctx, ilhan.ID, "github")
	require.NoError(t, err)
	assert.False(t, found)

	deleted, err := s.DeleteUser(ctx, "ilhan")
	require.NoError(t, err)
	require.True(t, deleted)
	_, ok, err = s.RepositoryUser(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.False(t, ok, "a deleted user's repositories go back to the server's token")
	accounts, err := s.ForgeAccounts(ctx, ilhan.ID)
	require.NoError(t, err)
	assert.Empty(t, accounts, "and their accounts go with them")
}
