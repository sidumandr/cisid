package api

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func request(h http.Handler, remote, forwarded string) int {
	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.RemoteAddr = remote
	if forwarded != "" {
		req.Header.Set("X-Forwarded-For", forwarded)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec.Code
}

func TestRateLimiterPerClient(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := NewRateLimiter(2, false) // burst 4
	rl.now = func() time.Time { return now }
	h := rl.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))

	for i := range 4 {
		assert.Equal(t, http.StatusOK, request(h, "10.0.0.1:5000", ""), "request %d within the burst", i)
	}
	assert.Equal(t, http.StatusTooManyRequests, request(h, "10.0.0.1:5001", ""))
	assert.Equal(t, http.StatusOK, request(h, "10.0.0.2:5000", ""), "other clients are not affected")

	now = now.Add(time.Second) // refills 2 tokens
	assert.Equal(t, http.StatusOK, request(h, "10.0.0.1:5000", ""))
	assert.Equal(t, http.StatusOK, request(h, "10.0.0.1:5000", ""))
	assert.Equal(t, http.StatusTooManyRequests, request(h, "10.0.0.1:5000", ""))
}

func TestRateLimiterResponse(t *testing.T) {
	rl := NewRateLimiter(1, false)
	h := rl.Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	request(h, "10.0.0.1:1", "")
	request(h, "10.0.0.1:1", "")

	req := httptest.NewRequest(http.MethodGet, "/api/jobs", nil)
	req.RemoteAddr = "10.0.0.1:1"
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusTooManyRequests, rec.Code)
	assert.Equal(t, "1", rec.Header().Get("Retry-After"))
}

func TestRateLimiterForwardedFor(t *testing.T) {
	h := func(trust bool) http.Handler {
		return NewRateLimiter(1, trust).Wrap(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	}

	// Behind a proxy every request comes from the proxy's address.
	trusted := h(true)
	for range 2 {
		assert.Equal(t, http.StatusOK, request(trusted, "10.0.0.9:1", "203.0.113.5, 10.0.0.9"))
	}
	assert.Equal(t, http.StatusTooManyRequests, request(trusted, "10.0.0.9:1", "203.0.113.5"))
	assert.Equal(t, http.StatusOK, request(trusted, "10.0.0.9:1", "198.51.100.7"), "each forwarded client has its own bucket")

	// Untrusted, the header is ignored: it could be forged to dodge the limit.
	untrusted := h(false)
	for range 2 {
		request(untrusted, "10.0.0.9:1", "203.0.113.1")
	}
	assert.Equal(t, http.StatusTooManyRequests, request(untrusted, "10.0.0.9:1", "198.51.100.2"))
}

func TestRateLimiterForgetsIdleClients(t *testing.T) {
	now := time.Unix(1000, 0)
	rl := NewRateLimiter(1, false)
	rl.now = func() time.Time { return now }
	rl.allow("10.0.0.1")

	now = now.Add(limiterIdle + 2*time.Minute)
	rl.allow("10.0.0.2")

	assert.NotContains(t, rl.clients, "10.0.0.1")
	assert.Contains(t, rl.clients, "10.0.0.2")
}
