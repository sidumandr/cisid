package db

import (
	"fmt"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStoreRegisterUser(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	u, first, err := s.RegisterUser(ctx, "ada", "hash", "")
	require.NoError(t, err)
	assert.True(t, first)
	assert.Equal(t, "admin", u.Role, "the first account is an admin")
	assert.True(t, u.HasPassword)

	_, _, err = s.RegisterUser(ctx, "eve", "hash", "")
	require.ErrorIs(t, err, ErrSignupClosed, "then sign-up is closed unless a role is given")

	u, first, err = s.RegisterUser(ctx, "bob", "hash", "viewer")
	require.NoError(t, err)
	assert.False(t, first)
	assert.Equal(t, "viewer", u.Role)

	_, _, err = s.RegisterUser(ctx, "bob", "hash", "viewer")
	require.ErrorIs(t, err, ErrUserExists)
}

func TestStoreRegisterUserHasOneFirst(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	var (
		wg     sync.WaitGroup
		mu     sync.Mutex
		admins int
	)
	for i := range 8 {
		wg.Go(func() {
			u, first, err := s.RegisterUser(ctx, fmt.Sprintf("user%d", i), "hash", "viewer")
			if !assert.NoError(t, err) {
				return
			}
			if first {
				mu.Lock()
				admins++
				mu.Unlock()
				assert.Equal(t, "admin", u.Role)
			}
		})
	}
	wg.Wait()
	assert.Equal(t, 1, admins, "however many sign up at once, one account is first")
	n, err := s.CountUsers(ctx)
	require.NoError(t, err)
	assert.Equal(t, 8, n)
}
