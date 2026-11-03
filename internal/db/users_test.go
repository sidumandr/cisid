package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreUsers(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	n, err := s.CountUsers(ctx)
	require.NoError(t, err)
	assert.Zero(t, n)

	ahmet, err := s.CreateUser(ctx, models.User{Username: "ahmet", Role: "maintainer",
		Identities: map[string]string{"gitea": "ahmet.y"}}, "hash-1")
	require.NoError(t, err)
	assert.True(t, ahmet.HasPassword)
	_, err = s.CreateUser(ctx, models.User{Username: "ayse", Role: "viewer"}, "")
	require.NoError(t, err)

	_, err = s.CreateUser(ctx, models.User{Username: "ahmet", Role: "viewer"}, "")
	assert.ErrorIs(t, err, ErrUserExists)
	_, err = s.CreateUser(ctx, models.User{Username: "other", Role: "viewer", Identities: map[string]string{"gitea": "ahmet.y"}}, "")
	assert.ErrorIs(t, err, ErrUserExists, "a forge login belongs to one user")

	users, err := s.Users(ctx)
	require.NoError(t, err)
	require.Len(t, users, 2)
	assert.Equal(t, "ahmet", users[0].Username)
	assert.Equal(t, map[string]string{"gitea": "ahmet.y"}, users[0].Identities)
	assert.False(t, users[1].HasPassword)

	got, hash, err := s.User(ctx, "ahmet")
	require.NoError(t, err)
	assert.Equal(t, "hash-1", hash)
	assert.Equal(t, "maintainer", got.Role)
	_, _, err = s.User(ctx, "nobody")
	assert.ErrorIs(t, err, ErrUserNotFound)

	admin, empty := "admin", ""
	updated, err := s.UpdateUser(ctx, "ayse", UserUpdate{Role: &admin, Identities: map[string]string{"gitlab": "ayse"}})
	require.NoError(t, err)
	assert.Equal(t, "admin", updated.Role)
	assert.Equal(t, map[string]string{"gitlab": "ayse"}, updated.Identities)
	updated, err = s.UpdateUser(ctx, "ahmet", UserUpdate{PasswordHash: &empty, Identities: map[string]string{}})
	require.NoError(t, err)
	assert.False(t, updated.HasPassword)
	assert.Empty(t, updated.Identities, "an empty map removes the identities")
	_, err = s.UpdateUser(ctx, "nobody", UserUpdate{Role: &admin})
	assert.ErrorIs(t, err, ErrUserNotFound)

	deleted, err := s.DeleteUser(ctx, "ayse")
	require.NoError(t, err)
	assert.True(t, deleted)
	deleted, err = s.DeleteUser(ctx, "ayse")
	require.NoError(t, err)
	assert.False(t, deleted)
}

func TestUserByIdentityBindsTheForgeID(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	u, err := s.CreateUser(ctx, models.User{Username: "ahmet", Role: "viewer", Identities: map[string]string{"github": "ahmet"}}, "")
	require.NoError(t, err)

	got, err := s.UserByIdentity(ctx, "github", "1001", "ahmet")
	require.NoError(t, err, "the login an admin named is bound on the first sign-in")
	assert.Equal(t, u.ID, got.ID)

	got, err = s.UserByIdentity(ctx, "github", "1001", "ahmet-renamed")
	require.NoError(t, err, "later sign-ins match the forge's id, whatever the login")
	assert.Equal(t, u.ID, got.ID)

	_, err = s.UserByIdentity(ctx, "github", "2002", "ahmet")
	assert.ErrorIs(t, err, ErrUserNotFound, "someone who takes the old login over is not let in")
	_, err = s.UserByIdentity(ctx, "gitlab", "1001", "ahmet")
	assert.ErrorIs(t, err, ErrUserNotFound, "identities are per forge")

	disabled := true
	_, err = s.UpdateUser(ctx, "ahmet", UserUpdate{Disabled: &disabled})
	require.NoError(t, err)
	_, err = s.UserByIdentity(ctx, "github", "1001", "ahmet-renamed")
	assert.ErrorIs(t, err, ErrUserNotFound, "a disabled user cannot sign in")
}

func TestSessionsAndTokens(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	u, err := s.CreateUser(ctx, models.User{Username: "ahmet", Role: "maintainer"}, "")
	require.NoError(t, err)

	require.NoError(t, s.CreateSession(ctx, "live", u.ID, time.Now().Add(time.Hour)))
	require.NoError(t, s.CreateSession(ctx, "old", u.ID, time.Now().Add(-time.Minute)))
	got, err := s.SessionUser(ctx, "live")
	require.NoError(t, err)
	assert.Equal(t, "ahmet", got.Username)
	_, err = s.SessionUser(ctx, "old")
	assert.ErrorIs(t, err, ErrUserNotFound, "an expired session signs nobody in")
	pruned, err := s.PruneSessions(ctx)
	require.NoError(t, err)
	assert.Equal(t, 1, pruned)
	require.NoError(t, s.DeleteSession(ctx, "live"))
	_, err = s.SessionUser(ctx, "live")
	assert.ErrorIs(t, err, ErrUserNotFound)

	tok, err := s.CreateAPIToken(ctx, u.ID, "deploy script", "token-digest")
	require.NoError(t, err)
	assert.Nil(t, tok.LastUsedAt)
	got, err = s.TokenUser(ctx, "token-digest")
	require.NoError(t, err)
	assert.Equal(t, u.ID, got.ID)
	tokens, err := s.APITokens(ctx, u.ID)
	require.NoError(t, err)
	require.Len(t, tokens, 1)
	assert.NotNil(t, tokens[0].LastUsedAt, "using a token is recorded")

	require.NoError(t, s.CreateSession(ctx, "again", u.ID, time.Now().Add(time.Hour)))
	disabled := true
	_, err = s.UpdateUser(ctx, "ahmet", UserUpdate{Disabled: &disabled})
	require.NoError(t, err)
	_, err = s.SessionUser(ctx, "again")
	assert.ErrorIs(t, err, ErrUserNotFound, "disabling a user ends their sessions")
	_, err = s.TokenUser(ctx, "token-digest")
	assert.ErrorIs(t, err, ErrUserNotFound, "and stops their tokens")

	ok, err := s.DeleteAPIToken(ctx, u.ID+1, tok.ID)
	require.NoError(t, err)
	assert.False(t, ok, "only the owner revokes a token")
	ok, err = s.DeleteAPIToken(ctx, u.ID, tok.ID)
	require.NoError(t, err)
	assert.True(t, ok)
}
