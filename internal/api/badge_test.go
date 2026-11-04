package api

import (
	"context"
	"encoding/xml"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/forge"
)

// memBadges answers BranchResult from a map of "repo branch" to the word.
type memBadges struct {
	results map[string]string
	repos   map[string]bool
	err     error
}

func (m memBadges) BranchResult(_ context.Context, repo, branch string) (bool, bool, error) {
	switch m.results[repo+" "+branch] {
	case "passing":
		return false, true, m.err
	case "failing":
		return true, true, m.err
	}
	return false, false, m.err
}

func (m memBadges) RepoHasJobs(_ context.Context, repo string) (bool, error) {
	return m.repos[repo], m.err
}

// countingResolver counts the lookups of default branches.
type countingResolver struct {
	mu    sync.Mutex
	calls int
	err   error
}

func (c *countingResolver) Repository(_ context.Context, _ string) (forge.Repository, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.calls++
	if c.err != nil {
		return forge.Repository{}, c.err
	}
	return forge.Repository{DefaultBranch: "main"}, nil
}

func (c *countingResolver) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.calls
}

func badgeRouter(store BadgeStore, repos RepoResolver, access *Access, public bool) (http.Handler, *BadgeAPI) {
	b := NewBadgeAPI(store, repos, access, public, slog.New(slog.DiscardHandler))
	return NewRouter(Handlers{
		Agents: http.NotFoundHandler(),
		Badges: b,
		Access: access,
		DB:     &mockPinger{},
	}, slog.New(slog.DiscardHandler)), b
}

func TestBadge(t *testing.T) {
	store := memBadges{
		results: map[string]string{"sidumandr/cisid main": "passing", "sidumandr/cisid dev": "failing", "gitlab:group/sub/app main": "failing"},
		repos:   map[string]bool{"sidumandr/cisid": true, "gitlab:group/sub/app": true},
	}
	resolver := &countingResolver{}
	h, _ := badgeRouter(store, resolver, nil, false)

	for target, want := range map[string]string{
		"/api/repos/sidumandr/cisid/badge.svg?branch=main":        "passing",
		"/api/repos/sidumandr/cisid/badge.svg?branch=dev":         "failing",
		"/api/repos/sidumandr/cisid/badge.svg?branch=gone":        "unknown",
		"/api/repos/gitlab:group%2Fsub/app/badge.svg?branch=main": "failing",
	} {
		rec := call(h, http.MethodGet, target, "", "")
		require.Equal(t, http.StatusOK, rec.Code, target)
		assert.Equal(t, "image/svg+xml", rec.Header().Get("Content-Type"))
		assert.Equal(t, "no-cache, max-age=0", rec.Header().Get("Cache-Control"))
		assert.Equal(t, "default-src 'none'", rec.Header().Get("Content-Security-Policy"))
		assert.Equal(t, `"`+want+`"`, rec.Header().Get("ETag"))
		assert.Contains(t, rec.Body.String(), ">"+want+"</text>", target)
		assert.Contains(t, rec.Body.String(), badgeStyles[want].color, target)
	}
	assert.Zero(t, resolver.count(), "a branch was given each time")

	for _, target := range []string{
		"/api/repos/sidumandr/cisid/badge.svg?branch=a%20b",
		"/api/repos/svn:x/y/badge.svg",
	} {
		assert.Equal(t, http.StatusBadRequest, call(h, http.MethodGet, target, "", "").Code, target)
	}
}

func TestBadgeNotModified(t *testing.T) {
	store := memBadges{results: map[string]string{"sidumandr/cisid main": "passing"}}
	h, _ := badgeRouter(store, &countingResolver{}, nil, false)

	get := func(etag string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, "/api/repos/sidumandr/cisid/badge.svg?branch=main", nil)
		req.Header.Set("If-None-Match", etag)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	rec := get(`"passing"`)
	assert.Equal(t, http.StatusNotModified, rec.Code)
	assert.Empty(t, rec.Body.String())
	assert.Equal(t, http.StatusOK, get(`"failing"`).Code, "the result changed")
}

func TestBadgeDefaultBranch(t *testing.T) {
	store := memBadges{
		results: map[string]string{"sidumandr/cisid main": "failing"},
		repos:   map[string]bool{"sidumandr/cisid": true},
	}
	resolver := &countingResolver{}
	h, b := badgeRouter(store, resolver, nil, false)
	now := time.Date(2026, 11, 4, 9, 0, 0, 0, time.UTC)
	b.now = func() time.Time { return now }
	const target = "/api/repos/sidumandr/cisid/badge.svg"

	for range 3 {
		rec := call(h, http.MethodGet, target, "", "")
		require.Equal(t, http.StatusOK, rec.Code)
		assert.Contains(t, rec.Body.String(), ">failing</text>", "the default branch")
	}
	assert.Equal(t, 1, resolver.count(), "the default branch is remembered")

	now = now.Add(defaultBranchTTL + time.Second)
	call(h, http.MethodGet, target, "", "")
	assert.Equal(t, 2, resolver.count(), "and looked up again later")

	rec := call(h, http.MethodGet, "/api/repos/someone/else/badge.svg", "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), ">unknown</text>")
	assert.Equal(t, 2, resolver.count(), "repositories cisid never built are not looked up")

	down := &countingResolver{err: forge.ErrTemporary}
	h, _ = badgeRouter(store, down, nil, false)
	rec = call(h, http.MethodGet, target, "", "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), ">unknown</text>", "the forge could not tell")
	call(h, http.MethodGet, target, "", "")
	assert.Equal(t, 1, down.count(), "a failed lookup is remembered too")
}

func TestBadgeAccess(t *testing.T) {
	store := memBadges{results: map[string]string{"sidumandr/cisid main": "passing"}}
	const target = "/api/repos/sidumandr/cisid/badge.svg?branch=main"

	private, _ := badgeRouter(store, &countingResolver{}, tokenAccess(readToken), false)
	assert.Equal(t, http.StatusUnauthorized, call(private, http.MethodGet, target, "", "").Code, "read like jobs")
	assert.Equal(t, http.StatusOK, call(private, http.MethodGet, target, readToken, "").Code)

	public, _ := badgeRouter(store, &countingResolver{}, tokenAccess(readToken), true)
	assert.Equal(t, http.StatusOK, call(public, http.MethodGet, target, "", "").Code, "public badges")
}

func TestBadgeStoreError(t *testing.T) {
	h, _ := badgeRouter(memBadges{err: errors.New("database down")}, &countingResolver{}, nil, false)
	assert.Equal(t, http.StatusInternalServerError,
		call(h, http.MethodGet, "/api/repos/sidumandr/cisid/badge.svg?branch=main", "", "").Code)
}

func TestBadgeSVGIsValid(t *testing.T) {
	for state, style := range badgeStyles {
		svg := badgeSVG(state)
		var doc struct {
			XMLName xml.Name `xml:"svg"`
			Width   int      `xml:"width,attr"`
			Title   string   `xml:"title"`
		}
		require.NoError(t, xml.Unmarshal([]byte(svg), &doc), state)
		assert.Equal(t, badgeLabelWidth+style.width, doc.Width)
		assert.Equal(t, "cisid: "+state, doc.Title)
	}
}
