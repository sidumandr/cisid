package auth

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRoles(t *testing.T) {
	assert.True(t, RoleAdmin.Allows(RoleMaintainer))
	assert.True(t, RoleMaintainer.Allows(RoleMaintainer))
	assert.True(t, RoleMaintainer.Allows(RoleViewer))
	assert.False(t, RoleViewer.Allows(RoleMaintainer))
	assert.False(t, Role("").Allows(RoleViewer))
	assert.False(t, Role("root").Allows(RoleViewer))

	r, err := ParseRole("maintainer")
	require.NoError(t, err)
	assert.Equal(t, RoleMaintainer, r)
	_, err = ParseRole("owner")
	assert.ErrorIs(t, err, ErrInvalidRole)
}

func TestValidUsername(t *testing.T) {
	for _, ok := range []string{"admin", "ahmet.yilmaz", "ci-bot", "a", strings.Repeat("a", 39)} {
		assert.True(t, ValidUsername(ok), ok)
	}
	for _, bad := range []string{"", "Admin", "-x", ".x", "a b", "a/b", strings.Repeat("a", 40), "ahmet@ofis"} {
		assert.False(t, ValidUsername(bad), bad)
	}
}

func TestPasswords(t *testing.T) {
	hash, err := HashPassword("correct horse battery")
	require.NoError(t, err)
	assert.True(t, strings.HasPrefix(hash, "pbkdf2-sha256$600000$"))
	assert.NotContains(t, hash, "correct horse")

	assert.True(t, CheckPassword(hash, "correct horse battery"))
	assert.False(t, CheckPassword(hash, "correct horse batter"))
	assert.False(t, CheckPassword("", "anything"), "a user without a password signs in some other way")
	assert.False(t, CheckPassword("md5$1$x$y", "anything"))

	again, err := HashPassword("correct horse battery")
	require.NoError(t, err)
	assert.NotEqual(t, hash, again, "each hash has its own salt")

	_, err = HashPassword("short")
	assert.ErrorIs(t, err, ErrWeakPassword)
}

func TestNewSecret(t *testing.T) {
	a, digest, err := NewSecret(TokenPrefix)
	require.NoError(t, err)
	b, _, err := NewSecret(TokenPrefix)
	require.NoError(t, err)

	assert.True(t, strings.HasPrefix(a, "cisid_"))
	assert.NotEqual(t, a, b)
	assert.Equal(t, digest, Digest(a))
	assert.Len(t, digest, 64)
	assert.NotContains(t, digest, a)
}
