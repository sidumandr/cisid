package api

import (
	"bufio"
	"context"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"runtime/debug"
	"strconv"
	"strings"
	"time"

	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"

	"github.com/sidumandr/cisid/internal/metrics"
)

const readyTimeout = 2 * time.Second

// Pinger reports whether a dependency is reachable.
type Pinger interface {
	Ping(ctx context.Context) error
}

// statusResponse is the JSON body of health checks and generic errors.
type statusResponse struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Handlers are the parts the router dispatches to.
type Handlers struct {
	Webhook http.Handler // GitHub webhook deliveries; omitted when nil
	// Webhooks are the other forges' deliveries, by forge name, served at
	// /webhooks/{forge}.
	Webhooks map[string]http.Handler
	Agents   http.Handler // agent WebSocket connections
	Jobs     *JobsAPI     // job and log API; omitted when nil
	Secrets  *SecretsAPI  // repository secrets API; omitted when nil
	UI       http.Handler // browser UI at /, /jobs/{id} and /ui/; omitted when nil
	DB       Pinger       // readiness check
	// RateLimit applies to the webhook and the API; nil means no limit.
	RateLimit *RateLimiter
	// Metrics serves GET /metrics; omitted when nil.
	Metrics http.Handler
	// Read guards jobs, logs and metrics and serves /api/login; nil lets
	// anyone read.
	Read *ReadAccess
	// Triggers serves manual builds and schedules; omitted when nil.
	Triggers *TriggersAPI
	// TracerProvider traces webhook and API requests; nil traces nothing.
	TracerProvider trace.TracerProvider
}

// NewRouter returns the server's HTTP handler:
//
//	POST /webhooks/github  GitHub webhook deliveries
//	POST /webhooks/{forge} Gitea, Forgejo, GitLab and Bitbucket deliveries
//	GET  /agent/ws         agent WebSocket connections
//	GET  /api/jobs/...     jobs and logs (see JobsAPI)
//	     /api/repos/...    repository secrets (see SecretsAPI)
//	GET  /, /jobs/{id}     browser UI
//	GET  /healthz          liveness: the process is serving requests
//	GET  /readyz           readiness: the database is reachable
func NewRouter(h Handlers, log *slog.Logger) http.Handler {
	// The webhook and the API are rate limited; agents, the UI's static
	// files and health checks are not.
	limited := http.NewServeMux()
	webhooks := maps.Clone(h.Webhooks)
	if h.Webhook != nil {
		if webhooks == nil {
			webhooks = map[string]http.Handler{}
		}
		webhooks["github"] = h.Webhook
	}
	for name, wh := range webhooks {
		limited.Handle("/webhooks/"+name, wh)
	}
	if h.Jobs != nil {
		h.Jobs.register(limited)
	}
	if h.Secrets != nil {
		h.Secrets.register(limited)
	}
	if h.Read != nil {
		h.Read.register(limited)
	}
	if h.Triggers != nil {
		h.Triggers.register(limited)
	}
	var limitedHandler http.Handler = limited
	if h.RateLimit != nil {
		limitedHandler = h.RateLimit.Wrap(limited)
	}

	mux := http.NewServeMux()
	mux.Handle("/webhooks/", limitedHandler)
	mux.Handle("/api/", limitedHandler)
	mux.Handle("GET /agent/ws", h.Agents)
	if h.Metrics != nil {
		mux.Handle("GET /metrics", h.Read.Wrap(h.Metrics))
	}
	if h.UI != nil {
		mux.Handle("GET /{$}", h.UI)
		mux.Handle("GET /jobs/{id}", h.UI)
		mux.Handle("GET /repos", h.UI)
		mux.Handle("GET /repos/{owner}/{repo}", h.UI)
		mux.Handle("GET /ui/", h.UI)
	}
	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
	})
	mux.HandleFunc("GET /readyz", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), readyTimeout)
		defer cancel()
		if err := h.DB.Ping(ctx); err != nil {
			log.WarnContext(ctx, "readiness check failed", "error", err)
			writeJSON(w, http.StatusServiceUnavailable,
				statusResponse{Status: "unavailable", Reason: "database unreachable"})
			return
		}
		writeJSON(w, http.StatusOK, statusResponse{Status: "ok"})
	})

	handler := logRequests(log, recoverPanics(log, mux))
	if h.TracerProvider == nil {
		return handler
	}
	return otelhttp.NewHandler(handler, "http request",
		otelhttp.WithTracerProvider(h.TracerProvider),
		otelhttp.WithPropagators(propagation.TraceContext{}),
		otelhttp.WithFilter(traced))
}

// traced reports whether a request gets a span: webhooks and short API
// calls do; agent connections and log streams, open for minutes, and the
// UI, metrics and health checks do not.
func traced(r *http.Request) bool {
	p := r.URL.Path
	return (strings.HasPrefix(p, "/webhooks/") || strings.HasPrefix(p, "/api/")) && !strings.HasSuffix(p, "/stream")
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
				statusResponse{Status: "error", Reason: "internal server error"})
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
		elapsed := time.Since(start)

		// The mux records the matched pattern on r, which keeps the label
		// set small (no IDs).
		route := r.Pattern
		if route == "" {
			route = "unmatched"
		}
		metrics.HTTPRequests.WithLabelValues(route, r.Method, strconv.Itoa(rec.status)).Inc()
		nameSpan(r, route)
		if !streaming(route) {
			metrics.HTTPDuration.WithLabelValues(route).Observe(elapsed.Seconds())
		}

		log.InfoContext(r.Context(), "http request",
			"method", r.Method,
			"path", r.URL.Path,
			"status", rec.status,
			"duration", elapsed,
			"remote_addr", r.RemoteAddr)
	})
}

// nameSpan names the request's span after the matched route, e.g.
// "GET /api/jobs/{id}", which the mux only knows after routing.
func nameSpan(r *http.Request, route string) {
	span := trace.SpanFromContext(r.Context())
	if !span.IsRecording() || r.Pattern == "" {
		return
	}
	// Patterns may start with a method ("GET /api/jobs/{id}").
	path := route
	if _, p, ok := strings.Cut(route, " "); ok {
		path = p
	}
	span.SetName(r.Method + " " + path)
	span.SetAttributes(attribute.String("http.route", path))
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

// streaming reports whether route holds a connection open (agents, log
// streams), which would only distort latency histograms.
func streaming(route string) bool {
	return strings.HasSuffix(route, "/stream") || strings.HasSuffix(route, "/agent/ws")
}
