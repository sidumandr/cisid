package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

// memRepos is an in-memory RepoStore.
type memRepos struct {
	mu      sync.Mutex
	repos   map[string]models.Repository
	watches map[string]models.Watch // "repo branch"
}

func (m *memRepos) PutRepository(_ context.Context, r models.Repository) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.repos == nil {
		m.repos = map[string]models.Repository{}
	}
	old, existed := m.repos[r.Repo]
	if existed {
		r.AddedBy = old.AddedBy
	}
	m.repos[r.Repo] = r
	return !existed, nil
}

func (m *memRepos) DeleteRepository(_ context.Context, repo string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, found := m.repos[repo]
	delete(m.repos, repo)
	for k, w := range m.watches {
		if w.Repo == repo {
			delete(m.watches, k)
			found = true
		}
	}
	return found, nil
}

func (m *memRepos) Repositories(context.Context) ([]models.Repository, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Repository
	for _, r := range m.repos {
		for _, w := range m.watches {
			r.Polled = r.Polled || w.Repo == r.Repo
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Repo < out[j].Repo })
	return out, nil
}

func (m *memRepos) PutWatch(_ context.Context, w models.Watch) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watches == nil {
		m.watches = map[string]models.Watch{}
	}
	_, existed := m.watches[w.Repo+" "+w.Branch]
	m.watches[w.Repo+" "+w.Branch] = w
	return !existed, nil
}

// stubForges knows sidumandr/biopotan, with a pipeline, and empty/repo,
// without one; anything else is not found.
type stubForges struct{ err error }

func (f stubForges) Repository(_ context.Context, key string) (forge.Repository, error) {
	if f.err != nil {
		return forge.Repository{}, f.err
	}
	if key != "sidumandr/biopotan" && key != "empty/repo" {
		return forge.Repository{}, forge.ErrNotFound
	}
	return forge.Repository{CloneURL: "https://github.com/" + key + ".git", DefaultBranch: "trunk"}, nil
}

func (f stubForges) BranchHead(context.Context, string, string) (string, error) { return headSHA, nil }

func (f stubForges) FileContent(_ context.Context, key, ref, file string) ([]byte, error) {
	if key == "empty/repo" || ref != headSHA || file != ".cisid.yml" {
		return nil, forge.ErrNotFound
	}
	return []byte("steps: []"), nil
}

func (f stubForges) Repositories(context.Context) forge.Listing {
	return forge.Listing{Repos: []forge.Listed{{Repo: "sidumandr/biopotan", Forge: forge.GitHub, Private: true}}, Unlisted: []string{forge.GitLab}}
}

func reposRouter(store RepoStore, forges RepoForges, access *Access, polling bool) http.Handler {
	return NewRouter(Handlers{
		Webhook: http.NotFoundHandler(),
		Agents:  http.NotFoundHandler(),
		Repos:   NewReposAPI(store, forges, access, slog.New(slog.DiscardHandler), polling),
		Access:  access,
		DB:      &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func TestConnectRepository(t *testing.T) {
	store := &memRepos{}
	h := reposRouter(store, stubForges{}, tokenAccess(readToken), true)

	rec := call(h, http.MethodGet, "/api/repos", readToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `[]`, rec.Body.String())

	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodPost, "/api/repos", "", `{"repo":"sidumandr/biopotan","poll":true}`).Code)
	assert.Equal(t, http.StatusForbidden, call(h, http.MethodPost, "/api/repos", readToken, `{"repo":"sidumandr/biopotan","poll":true}`).Code,
		"connecting needs a maintainer")

	rec = call(h, http.MethodPost, "/api/repos", writeToken, `{"repo":" sidumandr/biopotan ","poll":true}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got struct {
		models.Repository
		HasPipeline bool `json:"has_pipeline"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "sidumandr/biopotan", got.Repo)
	assert.Equal(t, "trunk", got.DefaultBranch)
	assert.Equal(t, "API token", got.AddedBy)
	assert.True(t, got.Polled)
	assert.True(t, got.HasPipeline)
	assert.Equal(t, "https://github.com/sidumandr/biopotan.git", store.watches["sidumandr/biopotan trunk"].CloneURL,
		"its default branch is polled, so its head builds")

	assert.Equal(t, http.StatusOK, call(h, http.MethodPost, "/api/repos", writeToken, `{"repo":"sidumandr/biopotan","poll":true}`).Code,
		"connecting again is no error")

	rec = call(h, http.MethodPost, "/api/repos", writeToken, `{"repo":"empty/repo","poll":false}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.False(t, got.HasPipeline, "a repository without .cisid.yml is connected, and says so")
	assert.False(t, got.Polled, "without polling it waits for a webhook")
	assert.NotContains(t, store.watches, "empty/repo trunk")

	rec = call(h, http.MethodGet, "/api/repos", readToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list []models.Repository
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 2)
	assert.Equal(t, []string{"empty/repo", "sidumandr/biopotan"}, []string{list[0].Repo, list[1].Repo})

	for body, want := range map[string]int{
		`{"repo":"nobody/nothing","poll":true}`: http.StatusNotFound,
		`{"repo":"not a key","poll":true}`:      http.StatusBadRequest,
		`{"repo":"svn:a/b","poll":true}`:        http.StatusBadRequest,
		`{"name":"x"}`:                          http.StatusBadRequest,
	} {
		assert.Equal(t, want, call(h, http.MethodPost, "/api/repos", writeToken, body).Code, body)
	}

	off := reposRouter(&memRepos{}, stubForges{}, tokenAccess(""), false)
	assert.Equal(t, http.StatusConflict, call(off, http.MethodPost, "/api/repos", writeToken, `{"repo":"sidumandr/biopotan","poll":true}`).Code,
		"no polling to connect it with")
	assert.Equal(t, http.StatusCreated, call(off, http.MethodPost, "/api/repos", writeToken, `{"repo":"sidumandr/biopotan","poll":false}`).Code)

	down := reposRouter(&memRepos{}, stubForges{err: forge.ErrTemporary}, tokenAccess(""), true)
	assert.Equal(t, http.StatusServiceUnavailable, call(down, http.MethodPost, "/api/repos", writeToken, `{"repo":"sidumandr/biopotan","poll":true}`).Code)
}

func TestDisconnectRepository(t *testing.T) {
	store := &memRepos{}
	h := reposRouter(store, stubForges{}, tokenAccess(readToken), true)
	require.Equal(t, http.StatusCreated, call(h, http.MethodPost, "/api/repos", writeToken, `{"repo":"sidumandr/biopotan","poll":true}`).Code)

	assert.Equal(t, http.StatusForbidden, call(h, http.MethodDelete, "/api/repos/sidumandr/biopotan", readToken, "").Code)
	assert.Equal(t, http.StatusNoContent, call(h, http.MethodDelete, "/api/repos/sidumandr/biopotan", writeToken, "").Code)
	assert.Empty(t, store.repos)
	assert.Empty(t, store.watches, "disconnecting stops the polling")
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodDelete, "/api/repos/sidumandr/biopotan", writeToken, "").Code)
}

func TestReachableRepositories(t *testing.T) {
	h := reposRouter(&memRepos{}, stubForges{}, tokenAccess(readToken), true)
	assert.Equal(t, http.StatusForbidden, call(h, http.MethodGet, "/api/forges/repositories", readToken, "").Code,
		"what the forges' tokens reach is for maintainers")
	rec := call(h, http.MethodGet, "/api/forges/repositories", writeToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.JSONEq(t, `{"repositories":[{"repo":"sidumandr/biopotan","forge":"github","private":true}],"unlisted":["gitlab"]}`, rec.Body.String())
}
