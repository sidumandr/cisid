package github

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

func TestFileContent(t *testing.T) {
	c := newClient(t, "ghp_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodGet, r.Method)
		assert.Equal(t, "/repos/sidumandr/cisid/contents/.cisid.yml", r.URL.Path)
		assert.Equal(t, sha, r.URL.Query().Get("ref"))
		assert.Equal(t, "application/vnd.github.raw+json", r.Header.Get("Accept"))
		assert.Equal(t, apiVersion, r.Header.Get("X-GitHub-Api-Version"))
		assert.Equal(t, "Bearer ghp_test", r.Header.Get("Authorization"))
		assert.NotEmpty(t, r.Header.Get("User-Agent"))
		_, _ = w.Write([]byte("steps: []\n"))
	})

	got, err := c.FileContent(t.Context(), "sidumandr/cisid", sha, ".cisid.yml")

	require.NoError(t, err)
	assert.Equal(t, "steps: []\n", string(got))
}

func TestFileContentWithoutToken(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Empty(t, r.Header.Get("Authorization"))
		_, _ = w.Write([]byte("ok"))
	})

	_, err := c.FileContent(t.Context(), "sidumandr/cisid", sha, ".cisid.yml")
	require.NoError(t, err)
}

func TestFileContentEscapesPath(t *testing.T) {
	c := newClient(t, "", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/repos/sidumandr/cisid/contents/ci/my%20file.yml", r.URL.EscapedPath())
		_, _ = w.Write([]byte("ok"))
	})

	_, err := c.FileContent(t.Context(), "sidumandr/cisid", sha, "ci/my file.yml")
	require.NoError(t, err)
}

func TestFileContentErrors(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
		want    error
		notWant error
	}{
		{
			name:    "missing file",
			handler: func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) },
			want:    ErrNotFound,
		},
		{
			name: "server error is temporary",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "unicorn", http.StatusBadGateway)
			},
			want: ErrTemporary,
		},
		{
			name: "secondary rate limit is temporary",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "slow down", http.StatusTooManyRequests)
			},
			want: ErrTemporary,
		},
		{
			name: "exhausted rate limit is temporary",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "0")
				http.Error(w, "API rate limit exceeded", http.StatusForbidden)
			},
			want: ErrTemporary,
		},
		{
			name: "forbidden is permanent",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-RateLimit-Remaining", "4999")
				http.Error(w, "Resource not accessible by integration", http.StatusForbidden)
			},
			notWant: ErrTemporary,
		},
		{
			name: "bad credentials are permanent",
			handler: func(w http.ResponseWriter, r *http.Request) {
				http.Error(w, "Bad credentials", http.StatusUnauthorized)
			},
			notWant: ErrTemporary,
		},
		{
			name: "file too large",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(strings.Repeat("x", maxFileBytes+1)))
			},
			want: ErrTooLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newClient(t, "", tt.handler)

			_, err := c.FileContent(t.Context(), "sidumandr/cisid", sha, ".cisid.yml")

			require.Error(t, err)
			if tt.want != nil {
				assert.ErrorIs(t, err, tt.want)
			}
			if tt.notWant != nil {
				assert.NotErrorIs(t, err, tt.notWant)
				assert.NotErrorIs(t, err, ErrNotFound)
			}
		})
	}
}

func TestFileContentUnreachableIsTemporary(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	_, err := (&Client{APIURL: url}).FileContent(t.Context(), "sidumandr/cisid", sha, ".cisid.yml")

	assert.ErrorIs(t, err, ErrTemporary)
}

func TestFileContentRejectsBadRepo(t *testing.T) {
	c := &Client{APIURL: "http://127.0.0.1:1"}
	for _, repo := range []string{"", "cisid", "a/b/c", "/cisid", "sidumandr/", "../etc"} {
		_, err := c.FileContent(t.Context(), repo, sha, ".cisid.yml")
		assert.ErrorIs(t, err, ErrInvalidRepo, "repo %q", repo)
	}
}

func TestCreateStatus(t *testing.T) {
	c := newClient(t, "ghp_test", func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, http.MethodPost, r.Method)
		assert.Equal(t, "/repos/sidumandr/cisid/statuses/"+sha, r.URL.Path)
		assert.Equal(t, "Bearer ghp_test", r.Header.Get("Authorization"))
		assert.Equal(t, "application/json", r.Header.Get("Content-Type"))
		var got commitStatus
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&got))
		assert.Equal(t, commitStatus{
			State: "success", TargetURL: "https://ci.example.com/jobs/42",
			Description: "Passed in 12s", Context: "cisid",
		}, got)
		w.WriteHeader(http.StatusCreated)
	})

	err := c.CreateStatus(t.Context(), "sidumandr/cisid", sha, forge.CommitStatus{
		State: forge.StateSuccess, TargetURL: "https://ci.example.com/jobs/42", Description: "Passed in 12s", Context: "cisid",
	})
	require.NoError(t, err)
}

func TestCreateStatusErrors(t *testing.T) {
	forbidden := newClient(t, "ghp_test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Resource not accessible by personal access token", http.StatusForbidden)
	})
	err := forbidden.CreateStatus(t.Context(), "sidumandr/cisid", sha, forge.CommitStatus{State: forge.StatePending, Context: "cisid"})
	require.Error(t, err)
	assert.NotErrorIs(t, err, ErrTemporary)
	assert.Contains(t, err.Error(), "Resource not accessible")

	down := newClient(t, "ghp_test", func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "unicorn", http.StatusServiceUnavailable)
	})
	err = down.CreateStatus(t.Context(), "sidumandr/cisid", sha, forge.CommitStatus{State: forge.StatePending, Context: "cisid"})
	assert.ErrorIs(t, err, ErrTemporary)

	err = forbidden.CreateStatus(t.Context(), "not-a-repo", sha, forge.CommitStatus{})
	assert.ErrorIs(t, err, ErrInvalidRepo)

	anonymous := &Client{APIURL: "http://127.0.0.1:1"}
	err = anonymous.CreateStatus(t.Context(), "sidumandr/cisid", sha, forge.CommitStatus{State: forge.StatePending})
	assert.ErrorIs(t, err, forge.ErrNoToken, "no request is sent without a token")
}

func TestCreateStatusMapsStates(t *testing.T) {
	var got []string
	c := newClient(t, "ghp_test", func(w http.ResponseWriter, r *http.Request) {
		var st commitStatus
		assert.NoError(t, json.NewDecoder(r.Body).Decode(&st))
		got = append(got, st.State)
		w.WriteHeader(http.StatusCreated)
	})
	for _, s := range []forge.State{forge.StatePending, forge.StateRunning, forge.StateSuccess,
		forge.StateFailure, forge.StateCanceled, forge.StateError} {
		require.NoError(t, c.CreateStatus(t.Context(), "sidumandr/cisid", sha, forge.CommitStatus{State: s, Context: "cisid"}))
	}
	assert.Equal(t, []string{"pending", "pending", "success", "failure", "error", "error"}, got)
}

func TestCloneCredentials(t *testing.T) {
	user, token := (&Client{}).CloneCredentials()
	assert.Empty(t, user+token, "public repositories clone anonymously")

	user, token = (&Client{Token: "ghp_test"}).CloneCredentials()
	assert.Equal(t, "x-access-token", user)
	assert.Equal(t, "ghp_test", token)
}
