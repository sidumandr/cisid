package github

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
)

func TestRepository(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/sidumandr/cisid", r.URL.Path)
		_, _ = w.Write([]byte(`{"full_name":"sidumandr/cisid","clone_url":"https://github.com/sidumandr/cisid.git","default_branch":"main"}`))
	})

	got, err := c.Repository(t.Context(), "sidumandr/cisid")

	require.NoError(t, err)
	assert.Equal(t, forge.Repository{CloneURL: "https://github.com/sidumandr/cisid.git", DefaultBranch: "main"}, got)
}

func TestBranchHead(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "application/vnd.github.sha", r.Header.Get("Accept"))
		switch r.URL.EscapedPath() {
		case "/repos/sidumandr/cisid/commits/feature/a%20b":
			_, _ = w.Write([]byte(sha))
		case "/repos/sidumandr/cisid/commits/odd":
			_, _ = w.Write([]byte("<html>"))
		default:
			http.NotFound(w, r)
		}
	})

	got, err := c.BranchHead(t.Context(), "sidumandr/cisid", "feature/a b")
	require.NoError(t, err)
	assert.Equal(t, sha, got, "slashes stay, other characters are escaped")

	_, err = c.BranchHead(t.Context(), "sidumandr/cisid", "gone")
	assert.ErrorIs(t, err, ErrNotFound)
	_, err = c.BranchHead(t.Context(), "sidumandr/cisid", "odd")
	assert.Error(t, err, "anything but a SHA is an error")
	_, err = c.BranchHead(t.Context(), "not a repo", "main")
	assert.ErrorIs(t, err, ErrInvalidRepo)
}
