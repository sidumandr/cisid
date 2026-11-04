package gitlab

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
	return &Client{URL: srv.URL, Token: token}
}

func TestRepositoryEscapesNestedPath(t *testing.T) {
	c := newClient(t, "glpat-test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/group%2Fsub%2Fproject", r.URL.EscapedPath())
		assert.Equal(t, "glpat-test", r.Header.Get("PRIVATE-TOKEN"))
		_, _ = w.Write([]byte(`{"http_url_to_repo":"https://gitlab.com/group/sub/project.git","default_branch":"main"}`))
	})

	got, err := c.Repository(t.Context(), "group/sub/project")

	require.NoError(t, err)
	assert.Equal(t, forge.Repository{CloneURL: "https://gitlab.com/group/sub/project.git", DefaultBranch: "main"}, got)
}

func TestBranchHead(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/group%2Fproject/repository/branches/feature%2Fx", r.URL.EscapedPath())
		assert.Empty(t, r.Header.Get("PRIVATE-TOKEN"))
		_, _ = w.Write([]byte(`{"commit":{"id":"` + sha + `"}}`))
	})

	got, err := c.BranchHead(t.Context(), "group/project", "feature/x")

	require.NoError(t, err)
	assert.Equal(t, sha, got)
}

func TestFileContentEscapesFilePath(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects/group%2Fproject/repository/files/ci%2Fpipeline.yml/raw", r.URL.EscapedPath())
		assert.Equal(t, sha, r.URL.Query().Get("ref"))
		_, _ = w.Write([]byte("steps: []\n"))
	})

	got, err := c.FileContent(t.Context(), "group/project", sha, "ci/pipeline.yml")

	require.NoError(t, err)
	assert.Equal(t, "steps: []\n", string(got))
}

func TestFileContentErrors(t *testing.T) {
	missing := newClient(t, "", func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) })
	_, err := missing.FileContent(t.Context(), "group/project", sha, ".cisid.yml")
	assert.ErrorIs(t, err, forge.ErrNotFound)

	limited := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("RateLimit-Remaining", "0")
		http.Error(w, "Retry later", http.StatusForbidden)
	})
	_, err = limited.FileContent(t.Context(), "group/project", sha, ".cisid.yml")
	assert.ErrorIs(t, err, forge.ErrTemporary)
}

func TestCreateStatus(t *testing.T) {
	var got []commitStatus
	c := newClient(t, "glpat-test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/api/v4/projects/group%2Fproject/statuses/"+sha, r.URL.EscapedPath())
		var st commitStatus
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&st))
		got = append(got, st)
		w.WriteHeader(http.StatusCreated)
	})

	for _, s := range []forge.State{forge.StatePending, forge.StateRunning, forge.StateSuccess, forge.StateFailure, forge.StateCanceled} {
		require.NoError(t, c.CreateStatus(t.Context(), "group/project", sha, forge.CommitStatus{State: s, Context: "cisid/pr"}))
	}

	var states []string
	for _, st := range got {
		states = append(states, st.State)
		assert.Equal(t, "cisid/pr", st.Name, "the context is the status name")
	}
	assert.Equal(t, []string{"pending", "running", "success", "failed", "canceled"}, states)
}

func TestCreateStatusNeedsToken(t *testing.T) {
	err := (&Client{URL: "http://127.0.0.1:1"}).CreateStatus(t.Context(), "group/project", sha, forge.CommitStatus{})
	assert.ErrorIs(t, err, forge.ErrNoToken)
}

func TestRejectsBadPath(t *testing.T) {
	for _, p := range []string{"", "project", "group//project", "../project"} {
		_, err := (&Client{}).Repository(t.Context(), p)
		assert.ErrorIs(t, err, forge.ErrInvalidRepo, p)
	}
}

func TestCloneCredentials(t *testing.T) {
	user, token := (&Client{Token: "glpat-test"}).CloneCredentials()
	assert.Equal(t, "oauth2", user)
	assert.Equal(t, "glpat-test", token)
}

func TestRepositoriesListsTheTokensProjects(t *testing.T) {
	c := newClient(t, "glpat", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/api/v4/projects", r.URL.Path)
		assert.Equal(t, "true", r.URL.Query().Get("membership"))
		assert.Equal(t, "glpat", r.Header.Get("PRIVATE-TOKEN"))
		_, _ = w.Write([]byte(`[{"path_with_namespace":"group/sub/app","visibility":"private"},{"path_with_namespace":"group/site","visibility":"public"}]`))
	})
	got, err := c.Repositories(t.Context())
	require.NoError(t, err)
	assert.Equal(t, []forge.ListedPath{{Path: "group/sub/app", Private: true}, {Path: "group/site"}}, got)

	_, err = (&Client{}).Repositories(t.Context())
	assert.ErrorIs(t, err, forge.ErrNoListing)
}
