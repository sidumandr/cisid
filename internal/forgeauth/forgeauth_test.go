package forgeauth

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// memStore is an in-memory Store.
type memStore struct {
	mu       sync.Mutex
	accounts map[string]db.SealedForgeAccount // "user forge"
	repos    map[string]int64
}

func key(userID int64, f string) string { return owner(userID) + " " + f }

func (m *memStore) PutForgeAccount(_ context.Context, a db.SealedForgeAccount) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.accounts == nil {
		m.accounts = map[string]db.SealedForgeAccount{}
	}
	m.accounts[key(a.UserID, a.Forge)] = a
	return nil
}

func (m *memStore) SealedForgeAccount(_ context.Context, userID int64, f string) (db.SealedForgeAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.accounts[key(userID, f)]
	if !ok {
		return db.SealedForgeAccount{}, db.ErrForgeAccountNotFound
	}
	return a, nil
}

func (m *memStore) ForgeAccounts(_ context.Context, userID int64) ([]models.ForgeAccount, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.ForgeAccount
	for _, a := range m.accounts {
		if a.UserID == userID {
			out = append(out, models.ForgeAccount{Forge: a.Forge, Login: a.Login, Kind: a.Kind})
		}
	}
	return out, nil
}

func (m *memStore) DeleteForgeAccount(_ context.Context, userID int64, f string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.accounts[key(userID, f)]
	delete(m.accounts, key(userID, f))
	return ok, nil
}

func (m *memStore) RepositoryUser(_ context.Context, repo string) (int64, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.repos[repo]
	return id, ok, nil
}

// fakeForges takes tokens that start with "good", and names them.
type fakeForges struct{}

func (fakeForges) Whoami(_ context.Context, name, token string) (string, error) {
	switch {
	case name == "bitbucket":
		return "", forge.ErrNoAccounts
	case token == "slow":
		return "", forge.ErrTemporary
	case len(token) >= 4 && token[:4] == "good":
		return "login-" + token, nil
	}
	return "", errors.New("unexpected response 401 Unauthorized")
}

func (fakeForges) Accounts() []string { return []string{"github", "gitlab"} }

func newService(t *testing.T, store *memStore, refreshers map[string]Refresher) *Service {
	t.Helper()
	c, err := secrets.NewCipher(make([]byte, 32))
	require.NoError(t, err)
	return New(store, c, fakeForges{}, refreshers, nil)
}

func TestConnectWithAToken(t *testing.T) {
	store := &memStore{}
	s := newService(t, store, nil)
	ctx := t.Context()

	a, err := s.ConnectToken(ctx, 7, "github", "good-pat")
	require.NoError(t, err)
	assert.Equal(t, "login-good-pat", a.Login)
	assert.Equal(t, KindToken, a.Kind)
	assert.NotContains(t, string(store.accounts[key(7, "github")].Token), "good-pat", "kept sealed")

	token, err := s.Token(ctx, 7, "github")
	require.NoError(t, err)
	assert.Equal(t, "good-pat", token)

	_, err = s.ConnectToken(ctx, 7, "github", "bad")
	assert.ErrorIs(t, err, ErrTokenRejected)
	_, err = s.ConnectToken(ctx, 7, "github", "slow")
	assert.ErrorIs(t, err, forge.ErrTemporary, "a forge that is down did not reject the token")
	_, err = s.ConnectToken(ctx, 7, "bitbucket", "good")
	assert.ErrorIs(t, err, forge.ErrNoAccounts)

	_, err = s.Token(ctx, 8, "github")
	assert.ErrorIs(t, err, ErrNotConnected, "another user's access is theirs")
	_, err = s.Token(ctx, 7, "gitlab")
	assert.ErrorIs(t, err, ErrNotConnected)

	// A token sealed for one user does not open as another's.
	stolen := store.accounts[key(7, "github")]
	stolen.UserID = 8
	require.NoError(t, store.PutForgeAccount(ctx, stolen))
	_, err = s.Token(ctx, 8, "github")
	assert.Error(t, err)

	found, err := s.Disconnect(ctx, 7, "github")
	require.NoError(t, err)
	assert.True(t, found)
	_, err = s.Token(ctx, 7, "github")
	assert.ErrorIs(t, err, ErrNotConnected)
}

func TestOAuthTokensAreRenewedBeforeTheyExpire(t *testing.T) {
	store := &memStore{}
	renewed := 0
	s := newService(t, store, map[string]Refresher{
		"gitlab": func(_ context.Context, refresh string) (auth.Token, error) {
			renewed++
			assert.Equal(t, "r1", refresh)
			return auth.Token{AccessToken: "good-a2", RefreshToken: "r2", Expiry: time.Now().Add(2 * time.Hour)}, nil
		},
	})
	ctx := t.Context()
	now := time.Now()
	s.now = func() time.Time { return now }

	_, err := s.ConnectOAuth(ctx, 7, "gitlab", auth.Token{AccessToken: "good-a1", RefreshToken: "r1", Expiry: now.Add(time.Hour)})
	require.NoError(t, err)
	token, err := s.Token(ctx, 7, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, "good-a1", token)
	assert.Zero(t, renewed, "not yet")

	now = now.Add(59 * time.Minute)
	token, err = s.Token(ctx, 7, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, "good-a2", token, "renewed just before it expires")
	assert.Equal(t, 1, renewed)
	token, err = s.Token(ctx, 7, "gitlab")
	require.NoError(t, err)
	assert.Equal(t, "good-a2", token, "and the new one kept")
	assert.Equal(t, 1, renewed)
}

func TestRepositoryTokenIsTheConnectorsAccess(t *testing.T) {
	store := &memStore{repos: map[string]int64{"sidumandr/biopotan": 7, "gitlab:group/app": 7}}
	s := newService(t, store, nil)
	ctx := t.Context()
	_, err := s.ConnectToken(ctx, 7, "github", "good-pat")
	require.NoError(t, err)

	token, ok := s.RepositoryToken(ctx, "sidumandr/biopotan")
	assert.True(t, ok)
	assert.Equal(t, "good-pat", token)

	_, ok = s.RepositoryToken(ctx, "gitlab:group/app")
	assert.False(t, ok, "the connector has no GitLab access: the server's token is used")
	_, ok = s.RepositoryToken(ctx, "sidumandr/cisid")
	assert.False(t, ok, "not connected by anyone's access")

	assert.Equal(t, []string{"github", "gitlab"}, s.Forges())
}
