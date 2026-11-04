package gitea

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
)

const sha = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"

func newClient(t *testing.T, token string, h http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(h)
	t.Cleanup(srv.Close)
	return &Client{URL: srv.URL + "/", Token: token}
}

func TestRepository(t *testing.T) {
	c := newClient(t, "gt_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/office/app", r.URL.Path)
		assert.Equal(t, "token gt_test", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"clone_url":"https://git.example.com/office/app.git","default_branch":"main"}`))
	})

	got, err := c.Repository(t.Context(), "office/app")

	require.NoError(t, err)
	assert.Equal(t, forge.Repository{CloneURL: "https://git.example.com/office/app.git", DefaultBranch: "main"}, got)
}

func TestBranchHead(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/office/app/branches/feature%2Fx", r.URL.EscapedPath())
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"name":"feature/x","commit":{"id":"` + sha + `"}}`))
	})

	got, err := c.BranchHead(t.Context(), "office/app", "feature/x")

	require.NoError(t, err)
	assert.Equal(t, sha, got)
}

func TestFileContent(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/repos/office/app/raw/ci/my%20file.yml", r.URL.EscapedPath())
		assert.Equal(t, sha, r.URL.Query().Get("ref"))
		_, _ = w.Write([]byte("steps: []\n"))
	})

	got, err := c.FileContent(t.Context(), "office/app", sha, "ci/my file.yml")

	require.NoError(t, err)
	assert.Equal(t, "steps: []\n", string(got))
}

func TestFileContentMissing(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })

	_, err := c.FileContent(t.Context(), "office/app", sha, ".cisid.yml")

	assert.ErrorIs(t, err, forge.ErrNotFound)
}

func TestCreateStatus(t *testing.T) {
	var got []commitStatus
	c := newClient(t, "gt_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v1/repos/office/app/statuses/"+sha, r.URL.Path)
		var st commitStatus
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&st))
		got = append(got, st)
		w.WriteHeader(http.StatusCreated)
	})

	for _, s := range []forge.State{forge.StateRunning, forge.StateFailure, forge.StateCanceled} {
		require.NoError(t, c.CreateStatus(t.Context(), "office/app", sha, forge.CommitStatus{
			State: s, TargetURL: "https://ci.example.com/jobs/1", Description: "d", Context: "cisid",
		}))
	}

	assert.Equal(t, []commitStatus{
		{State: "pending", TargetURL: "https://ci.example.com/jobs/1", Description: "d", Context: "cisid"},
		{State: "failure", TargetURL: "https://ci.example.com/jobs/1", Description: "d", Context: "cisid"},
		{State: "error", TargetURL: "https://ci.example.com/jobs/1", Description: "d", Context: "cisid"},
	}, got)
}

func TestCreateStatusNeedsToken(t *testing.T) {
	c := &Client{URL: "http://127.0.0.1:1"}
	err := c.CreateStatus(t.Context(), "office/app", sha, forge.CommitStatus{State: forge.StatePending})
	assert.ErrorIs(t, err, forge.ErrNoToken)
}

func TestRejectsBadRepo(t *testing.T) {
	c := &Client{URL: "http://127.0.0.1:1", Name: forge.Forgejo}
	for _, repo := range []string{"", "app", "a/b/c", "../etc"} {
		_, err := c.Repository(t.Context(), repo)
		assert.ErrorIs(t, err, forge.ErrInvalidRepo, repo)
	}
}

func TestCloneCredentials(t *testing.T) {
	user, token := (&Client{}).CloneCredentials()
	assert.Empty(t, user+token)
	user, token = (&Client{Token: "gt_test"}).CloneCredentials()
	assert.Equal(t, "oauth2", user)
	assert.Equal(t, "gt_test", token)
}

func TestRepositoriesListsTheTokensRepositories(t *testing.T) {
	c := newClient(t, "gitea-token", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v1/user/repos", r.URL.Path)
		assert.Equal(t, "token gitea-token", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`[{"full_name":"office/billing","private":true}]`))
	})
	got, err := c.Repositories(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []forge.ListedPath{{Path: "office/billing", Private: true}}, got)

	_, err = (&Client{}).Repositories(t.Context())
	assert.ErrorIs(t, err, forge.ErrNoListing)
}
