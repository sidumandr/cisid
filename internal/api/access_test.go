package api

import (
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

const readToken = "read-token-0123456789"

func readRouter(t *testing.T, read *ReadAccess) http.Handler {
	t.Helper()
	store := &mockJobReader{}
	store.On("ListJobs", mock.Anything, mock.Anything).Return([]models.Job{}, nil)
	store.On("CancelJob", mock.Anything, mock.Anything).Return(models.Job{ID: 1, Status: models.JobStatusCanceled}, nil)
	jobs := NewJobsAPI(store, &fakeEvents{}, slog.New(slog.DiscardHandler), WithWriteToken(writeToken), WithReadAccess(read))
	t.Cleanup(jobs.Close)
	return NewRouter(Handlers{
		Webhook: http.NotFoundHandler(),
		Agents:  http.NotFoundHandler(),
		Jobs:    jobs,
		Read:    read,
		Metrics: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("metrics")) }),
		DB:      &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func send(h http.Handler, req *http.Request) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func getAs(h http.Handler, target, token string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodGet, target, nil)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	return send(h, req)
}

func TestReadAccessOpenByDefault(t *testing.T) {
	h := readRouter(t, NewReadAccess("", writeToken))
	assert.Equal(t, http.StatusOK, getAs(h, "/api/jobs", "").Code)
	assert.Equal(t, http.StatusOK, getAs(h, "/metrics", "").Code)
}

func TestReadAccessNeedsAToken(t *testing.T) {
	h := readRouter(t, NewReadAccess(readToken, writeToken))

	for _, target := range []string{"/api/jobs", "/metrics"} {
		assert.Equal(t, http.StatusUnauthorized, getAs(h, target, "").Code, target)
		assert.Equal(t, http.StatusUnauthorized, getAs(h, target, "wrong-token-0123456789").Code, target)
		assert.Equal(t, http.StatusOK, getAs(h, target, readToken).Code, target)
		assert.Equal(t, http.StatusOK, getAs(h, target, writeToken).Code, "%s: the API token reads too", target)
	}
	assert.Equal(t, http.StatusOK, getAs(h, "/healthz", "").Code, "health checks stay open")
}

func TestReadAccessSignsInTheBrowser(t *testing.T) {
	h := readRouter(t, NewReadAccess(readToken, writeToken))
	login := func(token string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"token":"`+token+`"}`))
		req.Header.Set("X-Forwarded-Proto", "https")
		return send(h, req)
	}

	assert.Equal(t, http.StatusUnauthorized, login("wrong-token-0123456789").Code)
	assert.Equal(t, http.StatusBadRequest, send(h, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{}`))).Code)

	rec := login(writeToken)
	require.Equal(t, http.StatusNoContent, rec.Code)
	cookies := rec.Result().Cookies()
	require.Len(t, cookies, 1)
	c := cookies[0]
	assert.Equal(t, tokenCookie, c.Name)
	assert.NotContains(t, c.Value, writeToken, "the cookie never holds the token")
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure, "behind HTTPS")
	assert.Equal(t, http.SameSiteStrictMode, c.SameSite)

	withCookie := func(method, target string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, target, nil)
		req.AddCookie(c)
		return send(h, req)
	}
	assert.Equal(t, http.StatusOK, withCookie(http.MethodGet, "/api/jobs").Code)
	assert.Equal(t, http.StatusUnauthorized, withCookie(http.MethodPost, "/api/jobs/1/cancel").Code,
		"the cookie only reads, even when signed in with the API token")

	forged := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	forged.AddCookie(&http.Cookie{Name: tokenCookie, Value: readToken})
	assert.Equal(t, http.StatusUnauthorized, send(h, forged).Code, "the raw token is no cookie")

	rec = send(h, httptest.NewRequest(http.MethodPost, "/api/logout", nil))
	assert.Equal(t, http.StatusNoContent, rec.Code)
	require.Len(t, rec.Result().Cookies(), 1)
	assert.Negative(t, rec.Result().Cookies()[0].MaxAge, "logout drops the cookie")
}
