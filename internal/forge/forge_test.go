package forge

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestKeys(t *testing.T) {
	tests := []struct {
		forge, path, key string
	}{
		{GitHub, "sidumandr/cisid", "sidumandr/cisid"},
		{Gitea, "office/app", "gitea:office/app"},
		{Forgejo, "office/app", "forgejo:office/app"},
		{GitLab, "group/sub/project", "gitlab:group/sub/project"},
		{Bitbucket, "team/repo", "bitbucket:team/repo"},
	}
	for _, tt := range tests {
		assert.Equal(t, tt.key, Key(tt.forge, tt.path))
		f, p, err := SplitKey(tt.key)
		require.NoError(t, err, tt.key)
		assert.Equal(t, tt.forge, f)
		assert.Equal(t, tt.path, p)
	}
}

func TestSplitKeyRejectsBadKeys(t *testing.T) {
	for _, key := range []string{
		"", "cisid", "a/b/c", "/cisid", "sidumandr/", "../etc", "sidumandr/ci sid",
		"gitea:a/b/c", "bitbucket:a", "gitlab:a", "gitlab:a//b", "svn:a/b", ":a/b", "gitlab:../a",
	} {
		_, _, err := SplitKey(key)
		assert.ErrorIs(t, err, ErrInvalidRepo, "key %q", key)
	}
}

func TestPullRequestRef(t *testing.T) {
	assert.Equal(t, "refs/pull/7/head", PullRequestRef(GitHub, 7, "fix"))
	assert.Equal(t, "refs/pull/7/head", PullRequestRef(Forgejo, 7, "fix"))
	assert.Equal(t, "refs/merge-requests/7/head", PullRequestRef(GitLab, 7, "fix"))
	assert.Equal(t, "refs/heads/fix", PullRequestRef(Bitbucket, 7, "fix"))
}

type fakeClient struct {
	path string
	user string
}

func (f *fakeClient) Repository(_ context.Context, path string) (Repository, error) {
	f.path = path
	return Repository{CloneURL: "https://example.com/" + path, DefaultBranch: "main"}, nil
}

func (f *fakeClient) BranchHead(_ context.Context, path, _ string) (string, error) {
	f.path = path
	return "head", nil
}

func (f *fakeClient) FileContent(_ context.Context, path, _, _ string) ([]byte, error) {
	f.path = path
	return []byte("steps: []"), nil
}

func (f *fakeClient) CreateStatus(_ context.Context, path, _ string, _ CommitStatus) error {
	f.path = path
	return nil
}

func (f *fakeClient) CloneCredentials() (string, string) { return f.user, "token" }

func TestSetRoutesKeysToClients(t *testing.T) {
	gh, gl := &fakeClient{user: "x-access-token"}, &fakeClient{user: "oauth2"}
	s := NewSet()
	s.Add(GitHub, gh)
	s.Add(GitLab, gl)
	ctx := t.Context()

	_, err := s.Repository(ctx, "sidumandr/cisid")
	require.NoError(t, err)
	assert.Equal(t, "sidumandr/cisid", gh.path)

	_, err = s.FileContent(ctx, "gitlab:group/sub/project", "main", ".cisid.yml")
	require.NoError(t, err)
	assert.Equal(t, "group/sub/project", gl.path, "the client gets the path without the forge")

	user, _ := s.CloneCredentials(ctx, "gitlab:group/project")
	assert.Equal(t, "oauth2", user)
	assert.Equal(t, []string{GitHub, GitLab}, s.Names())

	_, err = s.BranchHead(ctx, "gitea:office/app", "main")
	assert.ErrorIs(t, err, ErrUnknownForge)
	err = s.CreateStatus(ctx, "not a key", "sha", CommitStatus{})
	assert.ErrorIs(t, err, ErrInvalidRepo)
	user, token := s.CloneCredentials(ctx, "gitea:office/app")
	assert.Empty(t, user+token)
}

func TestCheckStatus(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		header  map[string]string
		want    error
		notWant error
	}{
		{name: "ok", status: http.StatusOK},
		{name: "not found", status: http.StatusNotFound, want: ErrNotFound},
		{name: "rate limited", status: http.StatusTooManyRequests, want: ErrTemporary},
		{name: "server error", status: http.StatusBadGateway, want: ErrTemporary},
		{name: "GitHub limit used up", status: http.StatusForbidden, header: map[string]string{"X-RateLimit-Remaining": "0"}, want: ErrTemporary},
		{name: "GitLab limit used up", status: http.StatusForbidden, header: map[string]string{"RateLimit-Remaining": "0"}, want: ErrTemporary},
		{name: "forbidden", status: http.StatusForbidden, notWant: ErrTemporary},
		{name: "unauthorized", status: http.StatusUnauthorized, notWant: ErrTemporary},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := httptest.NewRecorder()
			for k, v := range tt.header {
				rec.Header().Set(k, v)
			}
			rec.WriteHeader(tt.status)
			err := CheckStatus(rec.Result())
			switch {
			case tt.want != nil:
				assert.ErrorIs(t, err, tt.want)
			case tt.notWant != nil:
				require.Error(t, err)
				assert.False(t, errors.Is(err, tt.notWant) || errors.Is(err, ErrNotFound))
			default:
				assert.NoError(t, err)
			}
		})
	}
}

func TestAPIFileIsLimited(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write(make([]byte, MaxFileBytes+1))
	}))
	defer srv.Close()

	_, err := API{Base: srv.URL}.File(t.Context(), "/big", "")
	assert.ErrorIs(t, err, ErrTooLarge)
}

func TestAPIUnreachableIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()

	err := API{Base: srv.URL}.GetJSON(t.Context(), "/x", &struct{}{})
	assert.ErrorIs(t, err, ErrTemporary)
}

func TestWebURL(t *testing.T) {
	u, err := WebURL("https://git.example.com/")
	require.NoError(t, err)
	assert.Equal(t, "https://git.example.com", u)
	for _, bad := range []string{"", "git.example.com", "ftp://git.example.com", "https://"} {
		_, err := WebURL(bad)
		assert.Error(t, err, bad)
	}
}
