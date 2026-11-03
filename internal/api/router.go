package api

import (
	"bufio"
	"context"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
	"time"
)

const readyTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// healthResponse is the JSON body of health checks and generic errors.
type healthResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// NewRouter returns the server's HTTP handler:
//
//	POST /webhooks/github  GitHub webhook deliveries
//	GET  /agent/ws         agent WebSocket connections
//	GET  /healthz          liveness: the process is serving requests
//	GET  /readyz           readiness: the database is reachable
func NewRouter(webhook, agents http.Handler, db Pinger, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/webhooks/github", webhook)
	mux.Handle("GET /agent/ws", agents)
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := db.Ping(ctx); err != nil {
			log.WarnContext(ctx, "readiness check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable,
				healthResponse{Status: "unavailable", Reason: "database unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, healthResponse{Status: "ok"})
	})

	return logRequests(log, recoverPanics(log, mux))
}

// recoverPanics turns a handler panic into a 500 response instead of a
// dropped connection.
func recoverPanics(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			rec := recover()
			if rec == nil {
				return
			}
			if rec == http.ErrAbortHandler {
				panic(rec)
			}
			log.ErrorContext(r.Context(), "handler panic",
				"method", r.Method, "path", r.URL.Path, "panic", rec, "stack", string(debug.Stack()))
			writeJSON(w, http.StatusInternalServerError,
				healthResponse{Status: "error", Reason: "internal server error"})
		}()
		next.ServeHTTP(w, r)
	})
}

// logRequests logs one line per request with its status and duration.
func logRequests(log *slog.Logger, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)
		log.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", time.Since(start),
			"remote_addr", r.RemoteAddr)
	})
}

// statusRecorder captures the response status. It forwards Hijack and Flush
// so WebSocket upgrades and streaming keep working behind it.
type statusRecorder struct {
	http.ResponseWriter
	status      int
	wroteHeader bool
}

func (r *statusRecorder) WriteHeader(status int) {
	if !r.wroteHeader {
		r.status, r.wroteHeader = status, true
	}
	r.ResponseWriter.WriteHeader(status)
}

func (r *statusRecorder) Write(p []byte) (int, error) {
	r.wroteHeader = true
	return r.ResponseWriter.Write(p)
}

func (r *statusRecorder) Unwrap() http.ResponseWriter {
	return r.ResponseWriter
}

func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	r.status, r.wroteHeader = http.StatusSwitchingProtocols, true
	return http.NewResponseController(r.ResponseWriter).Hijack()
}

func (r *statusRecorder) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}
