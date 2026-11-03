package secrets

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/pkg/models"
)

func newKey(t *testing.T) []byte {
	t.Helper()
	key := make([]byte, KeySize)
	_, err := rand.Read(key)
	require.NoError(t, err)
	return key
}

func newCipher(t *testing.T) *Cipher {
	t.Helper()
	c, err := NewCipher(newKey(t))
	require.NoError(t, err)
	return c
}

func TestParseKey(t *testing.T) {
	key := newKey(t)
	got, err := ParseKey(" " + base64.StdEncoding.EncodeToString(key) + "\n")
	require.NoError(t, err)
	assert.Equal(t, key, got)

	for _, bad := range []string{"", "not base64!", base64.StdEncoding.EncodeToString(key[:16])} {
		_, err := ParseKey(bad)
		assert.ErrorIs(t, err, ErrInvalidKey, "%q", bad)
	}
}

func TestCipherRoundTrip(t *testing.T) {
	c := newCipher(t)

	sealed, err := c.Seal("sidumandr/cisid", "TOKEN", "s3cret value")
	require.NoError(t, err)
	assert.NotContains(t, string(sealed), "s3cret")

	again, err := c.Seal("sidumandr/cisid", "TOKEN", "s3cret value")
	require.NoError(t, err)
	assert.NotEqual(t, sealed, again, "every seal uses a fresh nonce")

	got, err := c.Open("sidumandr/cisid", "TOKEN", sealed)
	require.NoError(t, err)
	assert.Equal(t, "s3cret value", got)
}

func TestCipherRejectsMovedOrTamperedValues(t *testing.T) {
	c := newCipher(t)
	sealed, err := c.Seal("sidumandr/cisid", "TOKEN", "value")
	require.NoError(t, err)

	_, err = c.Open("other/repo", "TOKEN", sealed)
	assert.ErrorIs(t, err, ErrCorrupt, "bound to its repository")
	_, err = c.Open("sidumandr/cisid", "OTHER", sealed)
	assert.ErrorIs(t, err, ErrCorrupt, "bound to its name")

	tampered := append([]byte(nil), sealed...)
	tampered[len(tampered)-1] ^= 1
	_, err = c.Open("sidumandr/cisid", "TOKEN", tampered)
	assert.ErrorIs(t, err, ErrCorrupt)

	_, err = newCipher(t).Open("sidumandr/cisid", "TOKEN", sealed)
	assert.ErrorIs(t, err, ErrCorrupt, "another key")

	_, err = c.Open("sidumandr/cisid", "TOKEN", []byte("short"))
	assert.ErrorIs(t, err, ErrCorrupt)
}

func TestValidateName(t *testing.T) {
	for _, ok := range []string{"TOKEN", "npm_token", "_X", "A1"} {
		assert.NoError(t, ValidateName(ok), ok)
	}
	for _, bad := range []string{"", "1TOKEN", "MY-TOKEN", "A B", "CI", "CISID", "CISID_X", strings.Repeat("A", 101)} {
		assert.ErrorIs(t, ValidateName(bad), ErrInvalidName, bad)
	}
}

func TestVault(t *testing.T) {
	pool := dbtest.New(t)
	_, err := db.Migrate(t.Context(), pool)
	require.NoError(t, err)
	store := db.New(pool)
	v := NewVault(store, newCipher(t))
	ctx := context.Background()
	const repo = "sidumandr/cisid"

	require.NoError(t, v.Set(ctx, repo, "DEPLOY_TOKEN", "abc123"))
	require.NoError(t, v.Set(ctx, repo, "NPM_TOKEN", "npm_xyz"))

	got, err := v.Secrets(ctx, repo, []string{"DEPLOY_TOKEN", "NPM_TOKEN"})
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"DEPLOY_TOKEN": "abc123", "NPM_TOKEN": "npm_xyz"}, got)

	_, err = v.Secrets(ctx, repo, []string{"DEPLOY_TOKEN", "MISSING"})
	assert.ErrorIs(t, err, ErrNotFound)

	// The database only ever holds ciphertext.
	sealed, err := store.SealedSecrets(ctx, repo, []string{"DEPLOY_TOKEN"})
	require.NoError(t, err)
	assert.NotContains(t, string(sealed["DEPLOY_TOKEN"]), "abc123")

	infos, err := v.List(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, []string{"DEPLOY_TOKEN", "NPM_TOKEN"}, names(infos))

	require.NoError(t, v.Delete(ctx, repo, "NPM_TOKEN"))
	assert.ErrorIs(t, v.Delete(ctx, repo, "NPM_TOKEN"), ErrNotFound)

	assert.ErrorIs(t, v.Set(ctx, repo, "BAD-NAME", "x"), ErrInvalidName)
	assert.ErrorIs(t, v.Set(ctx, repo, "BIG", strings.Repeat("x", MaxValueBytes+1)), ErrTooLarge)
}

func names(infos []models.SecretInfo) []string {
	var out []string
	for _, i := range infos {
		out = append(out, i.Name)
	}
	return out
}
