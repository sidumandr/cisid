package api

import (
	"net"
	"net/http"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

const limiterIdle = 10 * time.Minute

// RateLimiter limits requests per client IP with a token bucket.
type RateLimiter struct {
	limit      rate.Limit
	burst      int
	trustProxy bool

	mu        sync.Mutex
	clients   map[string]*client
	lastSweep time.Time
	now       func() time.Time
}

type client struct {
	limiter  *rate.Limiter
	lastSeen time.Time
}

// NewRateLimiter allows each client perSecond requests per second with
// bursts of twice that. With trustProxy the client is the first address
// in X-Forwarded-For, for use behind a reverse proxy that sets it.
func NewRateLimiter(perSecond float64, trustProxy bool) *RateLimiter {
	return &RateLimiter{
		limit:      rate.Limit(perSecond),
		burst:      max(1, int(2*perSecond)),
		trustProxy: trustProxy,
		clients:    map[string]*client{},
		now:        time.Now,
	}
}

// Wrap rejects requests over the limit with 429 Too Many Requests.
func (rl *RateLimiter) Wrap(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !rl.allow(rl.clientIP(r)) {
			w.Header().Set("Retry-After", "1")
			writeError(w, http.StatusTooManyRequests, "too many requests")
			return
		}
		next.ServeHTTP(w, r)
	})
}

func (rl *RateLimiter) allow(ip string) bool {
	now := rl.now()
	rl.mu.Lock()
	defer rl.mu.Unlock()

	// Forget idle clients now and then so the map stays small.
	if now.Sub(rl.lastSweep) > time.Minute {
		for k, c := range rl.clients {
			if now.Sub(c.lastSeen) > limiterIdle {
				delete(rl.clients, k)
			}
		}
		rl.lastSweep = now
	}

	c, ok := rl.clients[ip]
	if !ok {
		c = &client{limiter: rate.NewLimiter(rl.limit, rl.burst)}
		rl.clients[ip] = c
	}
	c.lastSeen = now
	return c.limiter.AllowN(now, 1)
}

func (rl *RateLimiter) clientIP(r *http.Request) string {
	if rl.trustProxy {
		if fwd := r.Header.Get("X-Forwarded-For"); fwd != "" {
			first, _, _ := strings.Cut(fwd, ",")
			return strings.TrimSpace(first)
		}
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
