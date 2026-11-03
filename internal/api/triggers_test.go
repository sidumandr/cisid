package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/pkg/models"
)

const headSHA = "6113728f27ae82c7b1a177c8d03f9e96e0adf246"

// memTriggers is an in-memory TriggerStore.
type memTriggers struct {
	mu        sync.Mutex
	jobs      []models.Job
	schedules map[string]models.Schedule // "repo name"
	watches   map[string]models.Watch    // "repo branch"
}

func (m *memTriggers) PutWatch(_ context.Context, w models.Watch) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.watches == nil {
		m.watches = map[string]models.Watch{}
	}
	_, existed := m.watches[w.Repo+" "+w.Branch]
	m.watches[w.Repo+" "+w.Branch] = w
	return !existed, nil
}

func (m *memTriggers) DeleteWatch(_ context.Context, repo, branch string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.watches[repo+" "+branch]
	delete(m.watches, repo+" "+branch)
	return ok, nil
}

func (m *memTriggers) Watches(_ context.Context, repo string) ([]models.Watch, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Watch
	for _, w := range m.watches {
		if w.Repo == repo {
			out = append(out, w)
		}
	}
	return out, nil
}

func (m *memTriggers) CreateJob(_ context.Context, job models.Job) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	job.ID = int64(len(m.jobs) + 1)
	m.jobs = append(m.jobs, job)
	return job.ID, nil
}

func (m *memTriggers) GetJob(_ context.Context, id int64) (models.Job, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.jobs[id-1], nil
}

func (m *memTriggers) PutSchedule(_ context.Context, s models.Schedule) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.schedules == nil {
		m.schedules = map[string]models.Schedule{}
	}
	m.schedules[s.Repo+" "+s.Name] = s
	return nil
}

func (m *memTriggers) DeleteSchedule(_ context.Context, repo, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.schedules[repo+" "+name]
	delete(m.schedules, repo+" "+name)
	return ok, nil
}

func (m *memTriggers) Schedules(_ context.Context, repo string) ([]models.Schedule, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Schedule
	for _, s := range m.schedules {
		if s.Repo == repo {
			out = append(out, s)
		}
	}
	return out, nil
}

type stubResolver struct{ err error }

func (r stubResolver) Repository(_ context.Context, repo string) (forge.Repository, error) {
	return forge.Repository{CloneURL: "https://github.com/" + repo + ".git", DefaultBranch: "main"}, r.err
}

func (r stubResolver) BranchHead(_ context.Context, _, branch string) (string, error) {
	if branch == "gone" {
		return "", forge.ErrNotFound
	}
	return headSHA, nil
}

func triggersRouter(store TriggerStore, resolver stubResolver, read *ReadAccess, changed func(int64), opts ...TriggersOption) http.Handler {
	a := NewTriggersAPI(store, resolver, writeToken, read, changed, slog.New(slog.DiscardHandler), opts...)
	a.now = func() time.Time { return time.Date(2026, 11, 3, 10, 30, 0, 0, time.UTC) }
	return NewRouter(Handlers{
		Webhook:  http.NotFoundHandler(),
		Agents:   http.NotFoundHandler(),
		Triggers: a,
		Read:     read,
		DB:       &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func call(h http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestManualBuild(t *testing.T) {
	store := &memTriggers{}
	var changed []int64
	h := triggersRouter(store, stubResolver{}, nil, func(id int64) { changed = append(changed, id) })
	const target = "/api/repos/sidumandr/cisid/builds"

	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodPost, target, "", `{}`).Code, "needs the write token")

	rec := call(h, http.MethodPost, target, writeToken, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var job models.Job
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &job))
	assert.Equal(t, "main", job.Branch, "no branch: the default branch")
	assert.Equal(t, models.EventManual, job.Event)
	assert.Equal(t, headSHA, job.CommitSHA)
	assert.Equal(t, "/api/jobs/1", rec.Header().Get("Location"))
	assert.Equal(t, []int64{1}, changed)

	rec = call(h, http.MethodPost, target, writeToken, `{"branch":"release/1.0"}`)
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "release/1.0", store.jobs[1].Branch)

	assert.Equal(t, http.StatusNotFound, call(h, http.MethodPost, target, writeToken, `{"branch":"gone"}`).Code)
	for _, body := range []string{`{"branch":"a b"}`, `{"branch":"../x"}`, `{"ref":"main"}`, `not json`} {
		assert.Equal(t, http.StatusBadRequest, call(h, http.MethodPost, target, writeToken, body).Code, body)
	}

	down := triggersRouter(&memTriggers{}, stubResolver{err: forge.ErrTemporary}, nil, nil)
	assert.Equal(t, http.StatusServiceUnavailable, call(down, http.MethodPost, target, writeToken, "").Code)
}

func TestManualBuildOfOtherForges(t *testing.T) {
	store := &memTriggers{}
	h := triggersRouter(store, stubResolver{}, nil, nil)

	rec := call(h, http.MethodPost, "/api/repos/gitlab:group%2Fsub/project/builds", writeToken, "")
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Equal(t, "gitlab:group/sub/project", store.jobs[0].Repo, "the owner segment carries the forge and the groups")

	rec = call(h, http.MethodPost, "/api/repos/gitea%3Aoffice/app/builds", writeToken, "")
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Equal(t, "gitea:office/app", store.jobs[1].Repo)

	for _, target := range []string{
		"/api/repos/svn:office/app/builds",      // unknown forge
		"/api/repos/gitea:a%2Fb/app/builds",     // too deep outside GitLab
		"/api/repos/sidumandr%2Fx/cisid/builds", // too deep on GitHub
	} {
		assert.Equal(t, http.StatusBadRequest, call(h, http.MethodPost, target, writeToken, "").Code, target)
	}

	unknown := triggersRouter(&memTriggers{}, stubResolver{err: forge.ErrUnknownForge}, nil, nil)
	assert.Equal(t, http.StatusNotFound, call(unknown, http.MethodPost, "/api/repos/forgejo:office/app/builds", writeToken, "").Code)
}

func TestWatchesAPI(t *testing.T) {
	store := &memTriggers{}
	h := triggersRouter(store, stubResolver{}, nil, nil, WithPolling(time.Minute))
	const base = "/api/repos/gitea:office/app/watches"

	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodPost, base, "", `{"branch":"main"}`).Code, "needs the write token")

	rec := call(h, http.MethodPost, base, writeToken, `{"branch":"release/1.0"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var got models.Watch
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	assert.Equal(t, "release/1.0", got.Branch, "the stored watch is returned")
	assert.Equal(t, "https://github.com/gitea:office/app.git", store.watches["gitea:office/app release/1.0"].CloneURL,
		"the clone URL comes from the forge")

	rec = call(h, http.MethodPost, base, writeToken, "")
	require.Equal(t, http.StatusCreated, rec.Code)
	assert.Contains(t, store.watches, "gitea:office/app main", "no branch: the default branch")
	assert.Equal(t, http.StatusOK, call(h, http.MethodPost, base, writeToken, `{"branch":"main"}`).Code, "watching twice is fine")
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodPost, base, writeToken, `{"branch":"gone"}`).Code)
	assert.Equal(t, http.StatusBadRequest, call(h, http.MethodPost, base, writeToken, `{"branch":"a b"}`).Code)

	rec = call(h, http.MethodGet, base, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list []models.Watch
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	assert.Len(t, list, 2)

	assert.Equal(t, http.StatusNoContent, call(h, http.MethodDelete, base+"/release/1.0", writeToken, "").Code,
		"branch names keep their slashes")
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodDelete, base+"/release/1.0", writeToken, "").Code)
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodDelete, base+"/main", "", "").Code)

	off := triggersRouter(&memTriggers{}, stubResolver{}, nil, nil)
	assert.Equal(t, http.StatusConflict, call(off, http.MethodPost, base, writeToken, `{"branch":"main"}`).Code,
		"nothing would poll the branch")
}

func TestSchedulesAPI(t *testing.T) {
	store := &memTriggers{}
	h := triggersRouter(store, stubResolver{}, NewReadAccess(readToken, writeToken), nil)
	const base = "/api/repos/sidumandr/cisid/schedules"

	rec := call(h, http.MethodPut, base+"/nightly", writeToken, `{"cron":"0 3 * * *","branch":"main"}`)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	var sch models.Schedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &sch))
	assert.Equal(t, time.Date(2026, 11, 4, 3, 0, 0, 0, time.UTC), sch.NextRunAt.UTC(), "the next run is computed")

	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, base, "", "").Code, "listing needs reading rights")
	rec = call(h, http.MethodGet, base, readToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	var list []models.Schedule
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "nightly", list[0].Name)

	for name, tc := range map[string]struct{ target, body string }{
		"bad cron":    {base + "/nightly", `{"cron":"daily"}`},
		"no cron":     {base + "/nightly", `{"branch":"main"}`},
		"bad name":    {base + "/Nightly%20Build", `{"cron":"@daily"}`},
		"bad branch":  {base + "/nightly", `{"cron":"@daily","branch":"a b"}`},
		"extra field": {base + "/nightly", `{"cron":"@daily","enabled":true}`},
	} {
		assert.Equal(t, http.StatusBadRequest, call(h, http.MethodPut, tc.target, writeToken, tc.body).Code, name)
	}
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodPut, base+"/x", readToken, `{"cron":"@daily"}`).Code,
		"the read token cannot change schedules")

	assert.Equal(t, http.StatusNoContent, call(h, http.MethodDelete, base+"/nightly", writeToken, "").Code)
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodDelete, base+"/nightly", writeToken, "").Code)
}
