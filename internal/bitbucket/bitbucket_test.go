package bitbucket

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
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
	return &Client{APIURL: srv.URL, Token: token}
}

func TestRepository(t *testing.T) {
	c := newClient(t, "bb_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repositories/team/app", r.URL.Path)
		assert.Equal(t, "Bearer bb_test", r.Header.Get("Authorization"))
		_, _ = w.Write([]byte(`{"full_name":"team/app","mainbranch":{"name":"main"}}`))
	})

	got, err := c.Repository(t.Context(), "team/app")

	require.NoError(t, err)
	assert.Equal(t, forge.Repository{CloneURL: "https://bitbucket.org/team/app.git", DefaultBranch: "main"}, got)
}

func TestBranchHead(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repositories/team/app/refs/branches/feature%2Fx", r.URL.EscapedPath())
		_, _ = w.Write([]byte(`{"name":"feature/x","target":{"hash":"` + sha + `"}}`))
	})

	got, err := c.BranchHead(t.Context(), "team/app", "feature/x")

	require.NoError(t, err)
	assert.Equal(t, sha, got)
}

func TestCommitResolvesAbbreviatedHash(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repositories/team/app/commit/6113728f27ae", r.URL.Path)
		_, _ = w.Write([]byte(`{"hash":"` + sha + `"}`))
	})

	got, err := c.Commit(t.Context(), "team/app", "6113728f27ae")

	require.NoError(t, err)
	assert.Equal(t, sha, got)
}

func TestFileContent(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repositories/team/app/src/"+sha+"/ci/my%20file.yml", r.URL.EscapedPath())
		_, _ = w.Write([]byte("steps: []\n"))
	})

	got, err := c.FileContent(t.Context(), "team/app", sha, "ci/my file.yml")

	require.NoError(t, err)
	assert.Equal(t, "steps: []\n", string(got))
}

func TestCreateStatus(t *testing.T) {
	var got []buildStatus
	c := newClient(t, "bb_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repositories/team/app/commit/"+sha+"/statuses/build", r.URL.Path)
		var st buildStatus
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&st))
		got = append(got, st)
		w.WriteHeader(http.StatusCreated)
	})

	require.NoError(t, c.CreateStatus(t.Context(), "team/app", sha, forge.CommitStatus{
		State: forge.StateRunning, TargetURL: "https://ci.example.com/jobs/1", Description: "Running", Context: "cisid",
	}))
	require.NoError(t, c.CreateStatus(t.Context(), "team/app", sha, forge.CommitStatus{
		State: forge.StateCanceled, Context: "cisid",
	}))

	assert.Equal(t, []buildStatus{
		{Key: "cisid", State: "INPROGRESS", Name: "cisid", URL: "https://ci.example.com/jobs/1", Description: "Running"},
		{Key: "cisid", State: "STOPPED", Name: "cisid", URL: "https://bitbucket.org/team/app"},
	}, got, "without a job link the status links to the repository")
}

func TestStatusKeyFitsLimit(t *testing.T) {
	short := "cisid/pr (go=1.25)"
	assert.Equal(t, short, statusKey(short))

	long := "cisid/pr (" + strings.Repeat("os=linux, ", 10) + ")"
	k := statusKey(long)
	assert.LessOrEqual(t, len(k), maxKeyLen)
	assert.NotEqual(t, k, statusKey(long+"x"), "different contexts get different keys")
	assert.Equal(t, k, statusKey(long), "the key is stable")
}

func TestCreateStatusNeedsToken(t *testing.T) {
	err := (&Client{APIURL: "http://127.0.0.1:1"}).CreateStatus(t.Context(), "team/app", sha, forge.CommitStatus{})
	assert.ErrorIs(t, err, forge.ErrNoToken)
}

func TestRejectsBadRepo(t *testing.T) {
	for _, repo := range []string{"", "app", "a/b/c"} {
		_, err := (&Client{}).Repository(t.Context(), repo)
		assert.ErrorIs(t, err, forge.ErrInvalidRepo, repo)
	}
}

func TestCloneCredentials(t *testing.T) {
	user, token := (&Client{Token: "bb_test"}).CloneCredentials()
	assert.Equal(t, "x-token-auth", user)
	assert.Equal(t, "bb_test", token)
}
