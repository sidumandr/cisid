// Command server runs the cisid API server.
//
// Configuration is read from environment variables; see internal/config.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/trace"
	"golang.org/x/sync/errgroup"

	"github.com/sidumandr/cisid/internal/api"
	"github.com/sidumandr/cisid/internal/bitbucket"
	"github.com/sidumandr/cisid/internal/config"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/events"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/gitea"
	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/internal/gitlab"
	"github.com/sidumandr/cisid/internal/hub"
	"github.com/sidumandr/cisid/internal/metrics"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/internal/status"
	"github.com/sidumandr/cisid/internal/telemetry"
	"github.com/sidumandr/cisid/internal/trigger"
	"github.com/sidumandr/cisid/internal/web"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	dbConnectTimeout  = 10 * time.Second
	poolCloseTimeout  = 5 * time.Second
	traceFlushTimeout = 5 * time.Second
	deliveryRetention = 8 * 24 * time.Hour
	expireBatch       = 100
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, os.Getenv, os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "server:", err)
		os.Exit(1)
	}
}

// run starts the server and blocks until ctx is canceled and shutdown is done.
func run(ctx context.Context, getenv func(string) string, logOut io.Writer) error {
	cfg, err := config.Load(getenv)
	if err != nil {
		return err
	}
	log := slog.New(telemetry.LogHandler(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: cfg.LogLevel})))
	log.InfoContext(ctx, "server starting", "config", cfg)

	tracing, err := telemetry.Setup(ctx, "cisid-server", getenv)
	if err != nil {
		return fmt.Errorf("set up tracing: %w", err)
	}
	// Deferred first, so it runs last and exports the spans of shutdown too.
	defer flushTraces(ctx, tracing, log)
	// The HTTP and forge wrappers are left out entirely when tracing is off.
	var httpTracing trace.TracerProvider
	if tracing.Enabled() {
		httpTracing = tracing.Provider
		log.InfoContext(ctx, "tracing enabled", "endpoint", tracing.Endpoint)
	}

	pool, err := pgxpool.New(ctx, cfg.DatabaseURL)
	if err != nil {
		return fmt.Errorf("create database pool: %w", err)
	}
	defer closePool(pool, log)

	store := db.New(pool)
	pingCtx, cancel := context.WithTimeout(ctx, dbConnectTimeout)
	defer cancel()
	if err := store.Ping(pingCtx); err != nil {
		return err
	}

	applied, err := db.Migrate(ctx, pool)
	if err != nil {
		return fmt.Errorf("migrate database: %w", err)
	}
	log.InfoContext(ctx, "database ready", "migrations_applied", applied)

	var forgeHTTP *http.Client
	if httpTracing != nil {
		forgeHTTP = forge.TracedHTTPClient(httpTracing)
	}
	forges, bb := forgeClients(cfg, forgeHTTP)
	log.InfoContext(ctx, "forges", "names", forges.Names())

	// Interfaces stay nil (not a nil *Vault) when secrets are off.
	var (
		vault     api.SecretVault
		secretSrc pipeline.SecretSource
	)
	if cfg.SecretKey != nil {
		c, err := secrets.NewCipher(cfg.SecretKey)
		if err != nil {
			return err
		}
		v := secrets.NewVault(store, c)
		vault, secretSrc = v, v
	} else {
		log.InfoContext(ctx, "secrets disabled: CISID_SECRET_KEY is not set")
	}
	planner := pipeline.Planner{Configs: forges, Secrets: secretSrc, Clones: forges}

	// Commit statuses need a token with write access to them; forges
	// without one are skipped.
	var reporter *status.Reporter
	jobChanged := func(int64) {}
	if anyForgeToken(cfg) {
		reporter = status.New(store, forges, status.WithLogger(log), status.WithJobURL(jobURL(cfg.PublicURL)),
			status.WithTracerProvider(tracing.Provider))
		jobChanged = func(int64) { reporter.Wake() }
	} else {
		log.InfoContext(ctx, "commit statuses disabled: no forge token is set")
	}

	if len(cfg.AgentTokens) == 0 {
		log.WarnContext(ctx, "agents are not authenticated: set CISID_AGENT_TOKEN on the server and the agents")
	}
	agents := hub.New(store, planner, hub.WithLogger(log), hub.WithJobChanged(jobChanged),
		hub.WithAgentTokens(cfg.AgentTokens...), hub.WithTracerProvider(tracing.Provider))
	webhooks, err := webhookHandlers(cfg, wakeOnCreate{store, func(id int64) {
		agents.Wake()
		jobChanged(id)
	}}, bb, log)
	if err != nil {
		return err
	}
	if len(webhooks) == 0 {
		log.InfoContext(ctx, "no webhooks: set a forge's webhook secret, or build by hand and on schedules")
	}

	metrics.RegisterAgents(agents.Connected)
	metrics.RegisterJobCounts(store.CountJobsByStatus, log)

	broker := events.New(log)
	read := api.NewReadAccess(cfg.ReadToken, cfg.APIToken)
	jobsAPI := api.NewJobsAPI(store, broker, log,
		api.WithWriteToken(cfg.APIToken),
		api.WithReadAccess(read),
		api.WithJobChanged(func(id int64) {
			agents.Wake() // dispatches reruns and delivers cancels here at once
			jobChanged(id)
		}))
	queued := func(id int64) {
		agents.Wake()
		jobChanged(id)
	}
	triggersAPI := api.NewTriggersAPI(store, forges, cfg.APIToken, read, queued, log)
	scheduler := trigger.NewScheduler(store, forges, trigger.WithLogger(log), trigger.WithQueued(queued))

	srv := &http.Server{
		Handler: api.NewRouter(api.Handlers{
			Webhooks:  webhooks,
			Agents:    agents,
			Jobs:      jobsAPI,
			Secrets:   api.NewSecretsAPI(vault, cfg.APIToken, log),
			UI:        web.Handler(),
			RateLimit: rateLimiter(cfg),
			Metrics:   metrics.Handler(),
			Read:      read,
			Triggers:  triggersAPI,
			DB:        store,
			// Webhook and API requests start the traces jobs continue.
			TracerProvider: httpTracing,
		}, log),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		ErrorLog:          slog.NewLogLogger(log.Handler(), slog.LevelWarn),
	}
	// Live log streams would otherwise keep Shutdown waiting.
	srv.RegisterOnShutdown(jobsAPI.Close)
	ln, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.HTTPAddr, err)
	}
	if cfg.TLSCertFile != "" {
		cert, err := tls.LoadX509KeyPair(cfg.TLSCertFile, cfg.TLSKeyFile)
		if err != nil {
			_ = ln.Close()
			return fmt.Errorf("load TLS certificate: %w", err)
		}
		srv.TLSConfig = &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12}
		ln = tls.NewListener(ln, srv.TLSConfig)
	}

	// If one stops with an error, the others are shut down too.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return agents.Run(gctx) })
	if reporter != nil {
		g.Go(func() error { return reporter.Run(gctx) })
	}
	g.Go(func() error { return housekeep(gctx, store, cfg.LogRetention, log) })
	g.Go(func() error { return scheduler.Run(gctx) })
	g.Go(func() error {
		return broker.Listen(gctx, func(ctx context.Context) (*pgx.Conn, error) {
			c, err := pool.Acquire(ctx)
			if err != nil {
				return nil, err
			}
			// LISTEN state must not return to the pool.
			return c.Hijack(), nil
		})
	})
	g.Go(func() error { return serve(gctx, srv, ln, cfg.ShutdownTimeout, log) })
	if err := g.Wait(); err != nil {
		return err
	}
	log.InfoContext(ctx, "server stopped")
	return nil
}

// forgeClients returns the clients of the configured forges. Bitbucket's
// is returned on its own too: its webhooks expand abbreviated hashes.
func forgeClients(cfg config.Config, httpClient *http.Client) (*forge.Set, *bitbucket.Client) {
	s := forge.NewSet()
	s.Add(forge.GitHub, &github.Client{APIURL: cfg.GitHubAPIURL, Token: cfg.GitHubToken, HTTP: httpClient})
	if cfg.Gitea.Enabled() {
		s.Add(forge.Gitea, &gitea.Client{URL: cfg.Gitea.URL, Token: cfg.Gitea.Token, HTTP: httpClient, Name: forge.Gitea})
	}
	if cfg.Forgejo.Enabled() {
		s.Add(forge.Forgejo, &gitea.Client{URL: cfg.Forgejo.URL, Token: cfg.Forgejo.Token, HTTP: httpClient, Name: forge.Forgejo})
	}
	s.Add(forge.GitLab, &gitlab.Client{URL: cfg.GitLab.URL, Token: cfg.GitLab.Token, HTTP: httpClient})
	bb := &bitbucket.Client{APIURL: cfg.Bitbucket.URL, Token: cfg.Bitbucket.Token, HTTP: httpClient}
	s.Add(forge.Bitbucket, bb)
	return s, bb
}

func anyForgeToken(cfg config.Config) bool {
	return cfg.GitHubToken != "" || cfg.Gitea.Token != "" || cfg.Forgejo.Token != "" ||
		cfg.GitLab.Token != "" || cfg.Bitbucket.Token != ""
}

// webhookHandlers returns a handler for every forge with a webhook secret,
// by forge name.
func webhookHandlers(cfg config.Config, jobs api.JobCreator, bb *bitbucket.Client, log *slog.Logger) (map[string]http.Handler, error) {
	secrets := map[string]string{
		forge.GitHub:    cfg.WebhookSecret,
		forge.Gitea:     cfg.Gitea.WebhookSecret,
		forge.Forgejo:   cfg.Forgejo.WebhookSecret,
		forge.GitLab:    cfg.GitLab.WebhookSecret,
		forge.Bitbucket: cfg.Bitbucket.WebhookSecret,
	}
	handlers := map[string]http.Handler{}
	for name, secret := range secrets {
		if secret == "" {
			continue
		}
		h, err := api.NewWebhookHandler([]byte(secret), jobs, log, api.WithForkBuilds(cfg.BuildForks),
			api.WithForge(name), api.WithCommitResolver(bb))
		if err != nil {
			return nil, fmt.Errorf("create %s webhook handler: %w", name, err)
		}
		handlers[name] = h
	}
	return handlers, nil
}

// wakeOnCreate calls wake whenever the webhook queues or cancels a job, so
// the change is dispatched and reported right away instead of on the next
// poll.
type wakeOnCreate struct {
	jobs api.JobCreator
	wake func(jobID int64)
}

func (w wakeOnCreate) QueueJob(ctx context.Context, job models.Job, payloadSHA256, deliveryID string) (int64, error) {
	id, err := w.jobs.QueueJob(ctx, job, payloadSHA256, deliveryID)
	if err == nil {
		w.wake(id)
	}
	return id, err
}

func (w wakeOnCreate) CancelPullRequest(ctx context.Context, repo string, number int) ([]int64, error) {
	ids, err := w.jobs.CancelPullRequest(ctx, repo, number)
	for _, id := range ids {
		w.wake(id) // delivers the cancels and reports them at once
	}
	return ids, err
}

// rateLimiter returns the configured limiter, or nil when disabled.
func rateLimiter(cfg config.Config) *api.RateLimiter {
	if cfg.RateLimit == 0 {
		return nil
	}
	return api.NewRateLimiter(cfg.RateLimit, cfg.TrustProxy)
}

// housekeep runs hourly. It prunes old webhook deliveries, which outlive
// the 7-day replay window a little so a replay inside it is always caught,
// and deletes the logs of jobs that finished logRetention ago (0 keeps them).
func housekeep(ctx context.Context, store *db.Store, logRetention time.Duration, log *slog.Logger) error {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	for {
		pctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Minute)
		if n, err := store.PruneDeliveries(pctx, deliveryRetention); err != nil {
			log.WarnContext(pctx, "prune webhook deliveries", "error", err)
		} else if n > 0 {
			log.InfoContext(pctx, "pruned webhook deliveries", "count", n)
		}
		if logRetention > 0 {
			expireLogs(pctx, store, logRetention, log)
		}
		cancel()

		select {
		case <-ctx.Done():
			return nil
		case <-tick.C:
		}
	}
}

// expireLogs deletes expired logs in batches, so no statement holds its
// locks for long; whatever does not fit in ctx's time waits for the next run.
func expireLogs(ctx context.Context, store *db.Store, retention time.Duration, log *slog.Logger) {
	total := 0
	for {
		n, err := store.ExpireLogs(ctx, retention, expireBatch)
		total += n
		if err != nil {
			log.WarnContext(ctx, "expire job logs", "error", err)
			break
		}
		if n < expireBatch {
			break
		}
	}
	if total > 0 {
		log.InfoContext(ctx, "deleted expired job logs", "jobs", total)
	}
}

// jobURL links commit statuses to the job when the public URL is known.
func jobURL(publicURL string) func(int64) string {
	if publicURL == "" {
		return nil
	}
	return func(id int64) string { return fmt.Sprintf("%s/jobs/%d", publicURL, id) }
}

// flushTraces exports the spans still buffered, but gives up after a few
// seconds so an unreachable collector cannot keep the process running.
func flushTraces(ctx context.Context, tracing *telemetry.Tracing, log *slog.Logger) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), traceFlushTimeout)
	defer cancel()
	if err := tracing.Shutdown(ctx); err != nil {
		log.WarnContext(ctx, "could not export all spans", "error", err)
	}
}

// closePool closes the database pool, but gives up waiting after a few
// seconds so a hung database connection cannot keep the server from exiting.
func closePool(pool *pgxpool.Pool, log *slog.Logger) {
	closed := make(chan struct{})
	go func() {
		defer close(closed)
		pool.Close()
	}()
	select {
	case <-closed:
	case <-time.After(poolCloseTimeout):
		log.Warn("database connections are still closing; exiting anyway")
	}
}

// serve runs srv on ln until ctx is canceled, then stops accepting
// connections and waits up to timeout for in-flight requests to finish.
func serve(ctx context.Context, srv *http.Server, ln net.Listener, timeout time.Duration, log *slog.Logger) error {
	served := make(chan error, 1)
	go func() { served <- srv.Serve(ln) }()
	log.InfoContext(ctx, "http server listening", "addr", ln.Addr().String())

	select {
	case err := <-served:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}

	log.InfoContext(ctx, "shutting down", "timeout", timeout)
	shutdownCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), timeout)
	defer cancel()
	if err := srv.Shutdown(shutdownCtx); err != nil {
		_ = srv.Close()
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	if err := <-served; !errors.Is(err, http.ErrServerClosed) {
		return fmt.Errorf("http server: %w", err)
	}
	return nil
}
