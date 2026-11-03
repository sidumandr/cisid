package api

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

type mockPinger struct {
	mock.Mock
}

func (m *mockPinger) Ping(ctx context.Context) error {
	return m.Called(ctx).Error(0)
}

func hasDeadline(ctx context.Context) bool {
	_, ok := ctx.Deadline()
	return ok
}

type routerFixture struct {
	handler http.Handler
	db      *mockPinger
	logs    *bytes.Buffer
	webhook http.HandlerFunc
	hooked  bool
	agents  bool
}

func newRouterFixture(t *testing.T) *routerFixture {
	t.Helper()
	f := &routerFixture{db: &mockPinger{}, logs: &bytes.Buffer{}}
	f.webhook = func(w http.ResponseWriter, r *http.Request) {
		f.hooked = true
		w.WriteHeader(http.StatusAccepted)
	}
	log := slog.New(slog.NewTextHandler(f.logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	webhook := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { f.webhook(w, r) })
	agents := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		f.agents = true
		w.WriteHeader(http.StatusSwitchingProtocols)
	})
	f.handler = NewRouter(Handlers{Webhook: webhook, Agents: agents, DB: f.db}, log)
	return f
}

func (f *routerFixture) do(method, target string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	f.handler.ServeHTTP(rec, httptest.NewRequest(method, target, nil))
	return rec
}

func decodeHealth(t *testing.T, rec *httptest.ResponseRecorder) statusResponse {
	t.Helper()
	assert.Equal(t, "application/json", rec.Header().Get("Content-Type"))
	var got statusResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &got))
	return got
}

func TestHealthz(t *testing.T) {
	f := newRouterFixture(t)

	rec := f.do(http.MethodGet, "/healthz")

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Equal(t, statusResponse{Status: "ok"}, decodeHealth(t, rec))
	f.db.AssertNotCalled(t, "Ping", mock.Anything)
}

func TestReadyz(t *testing.T) {
	t.Run("database reachable", func(t *testing.T) {
		f := newRouterFixture(t)
		f.db.On("Ping", mock.MatchedBy(hasDeadline)).Return(nil).Once()

		rec := f.do(http.MethodGet, "/readyz")

		assert.Equal(t, http.StatusOK, rec.Code)
		assert.Equal(t, statusResponse{Status: "ok"}, decodeHealth(t, rec))
		f.db.AssertExpectations(t)
	})

	t.Run("database down", func(t *testing.T) {
		f := newRouterFixture(t)
		f.db.On("Ping", mock.Anything).Return(errors.New("connection refused")).Once()

		rec := f.do(http.MethodGet, "/readyz")

		assert.Equal(t, http.StatusServiceUnavailable, rec.Code)
		assert.Equal(t, statusResponse{Status: "unavailable", Reason: "database unreachable"}, decodeHealth(t, rec))
		assert.Contains(t, f.logs.String(), "connection refused")
		f.db.AssertExpectations(t)
	})
}

func TestRouterRoutes(t *testing.T) {
	t.Run("webhook", func(t *testing.T) {
		f := newRouterFixture(t)
		rec := f.do(http.MethodPost, "/webhooks/github")
		assert.Equal(t, http.StatusAccepted, rec.Code)
		assert.True(t, f.hooked)
	})

	t.Run("agent websocket", func(t *testing.T) {
		f := newRouterFixture(t)
		assert.Equal(t, http.StatusSwitchingProtocols, f.do(http.MethodGet, "/agent/ws").Code)
		assert.True(t, f.agents)
	})

	t.Run("agent websocket needs GET", func(t *testing.T) {
		f := newRouterFixture(t)
		assert.Equal(t, http.StatusMethodNotAllowed, f.do(http.MethodPost, "/agent/ws").Code)
		assert.False(t, f.agents)
	})

	t.Run("ui", func(t *testing.T) {
		ui := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusTeapot) })
		h := NewRouter(Handlers{Webhook: http.NotFoundHandler(), Agents: http.NotFoundHandler(), UI: ui, DB: &mockPinger{}},
			slog.New(slog.DiscardHandler))
		for _, path := range []string{"/", "/jobs/7", "/ui/app.js"} {
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
			assert.Equal(t, http.StatusTeapot, rec.Code, path)
		}
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/elsewhere", nil))
		assert.Equal(t, http.StatusNotFound, rec.Code)
	})

	t.Run("unknown path", func(t *testing.T) {
		f := newRouterFixture(t)
		assert.Equal(t, http.StatusNotFound, f.do(http.MethodGet, "/nope").Code)
	})

	t.Run("wrong method on health check", func(t *testing.T) {
		f := newRouterFixture(t)
		assert.Equal(t, http.StatusMethodNotAllowed, f.do(http.MethodPost, "/healthz").Code)
	})
}

func TestRouterRecoversFromPanics(t *testing.T) {
	f := newRouterFixture(t)
	f.webhook = func(http.ResponseWriter, *http.Request) { panic("boom") }

	rec := f.do(http.MethodPost, "/webhooks/github")

	assert.Equal(t, http.StatusInternalServerError, rec.Code)
	assert.Equal(t, statusResponse{Status: "error", Reason: "internal server error"}, decodeHealth(t, rec))
	assert.Contains(t, f.logs.String(), "boom")

	// The router keeps serving after a panic.
	assert.Equal(t, http.StatusOK, f.do(http.MethodGet, "/healthz").Code)
}

func TestRouterLogsRequests(t *testing.T) {
	f := newRouterFixture(t)

	f.do(http.MethodGet, "/healthz")

	line := f.logs.String()
	for _, want := range []string{"method=GET", "path=/healthz", "status=200", "duration="} {
		assert.Contains(t, line, want)
	}
}

func TestRouterAllowsHijacking(t *testing.T) {
	// WebSocket upgrades need the logging wrapper to pass Hijack through.
	f := newRouterFixture(t)
	f.webhook = func(w http.ResponseWriter, r *http.Request) {
		conn, rw, err := http.NewResponseController(w).Hijack()
		if !assert.NoError(t, err) {
			return
		}
		defer conn.Close()
		_, _ = rw.WriteString("HTTP/1.1 101 Switching Protocols\r\nConnection: close\r\n\r\n")
		_ = rw.Flush()
	}
	srv := httptest.NewServer(f.handler)
	defer srv.Close()

	resp, err := http.Post(srv.URL+"/webhooks/github", "application/json", strings.NewReader("{}"))
	require.NoError(t, err)
	defer resp.Body.Close()
	assert.Equal(t, http.StatusSwitchingProtocols, resp.StatusCode)
	_, _ = bufio.NewReader(resp.Body).ReadString('\n')
}

func TestRouterRateLimitsWebhookAndAPI(t *testing.T) {
	h := NewRouter(Handlers{
		Webhook:   http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		Agents:    http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}),
		DB:        &mockPinger{},
		RateLimit: NewRateLimiter(1, false), // burst 2
	}, slog.New(slog.DiscardHandler))
	call := func(method, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(method, path, nil))
		return rec.Code
	}

	assert.Equal(t, http.StatusOK, call(http.MethodPost, "/webhooks/github"))
	assert.Equal(t, http.StatusNotFound, call(http.MethodGet, "/api/nothing"))
	assert.Equal(t, http.StatusTooManyRequests, call(http.MethodPost, "/webhooks/github"))
	assert.Equal(t, http.StatusTooManyRequests, call(http.MethodGet, "/api/jobs"))

	for range 5 {
		assert.Equal(t, http.StatusOK, call(http.MethodGet, "/agent/ws"), "agents are not limited")
	}
}
