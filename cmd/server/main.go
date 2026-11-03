// Command server runs the cisid API server.
//
// Configuration is read from environment variables; see internal/config.
package main

import (
	"context"
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
	"golang.org/x/sync/errgroup"

	"github.com/sidumandr/cisid/internal/api"
	"github.com/sidumandr/cisid/internal/config"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/events"
	"github.com/sidumandr/cisid/internal/github"
	"github.com/sidumandr/cisid/internal/hub"
	"github.com/sidumandr/cisid/internal/pipeline"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/internal/status"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	dbConnectTimeout = 10 * time.Second
	poolCloseTimeout = 5 * time.Second
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
	log := slog.New(slog.NewTextHandler(logOut, &slog.HandlerOptions{Level: cfg.LogLevel}))
	log.InfoContext(ctx, "server starting", "config", cfg)

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

	gh := &github.Client{APIURL: cfg.GitHubAPIURL, Token: cfg.GitHubToken}

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
	planner := pipeline.Planner{Configs: gh, Secrets: secretSrc}

	// Commit statuses need a token with write access to them.
	var reporter *status.Reporter
	jobChanged := func(int64) {}
	if cfg.GitHubToken != "" {
		reporter = status.New(store, gh, status.WithLogger(log), status.WithJobURL(jobURL(cfg.PublicURL)))
		jobChanged = func(int64) { reporter.Wake() }
	} else {
		log.InfoContext(ctx, "commit statuses disabled: CISID_GITHUB_TOKEN is not set")
	}

	agents := hub.New(store, planner, hub.WithLogger(log), hub.WithJobChanged(jobChanged))
	webhook, err := api.NewWebhookHandler([]byte(cfg.WebhookSecret), wakeOnCreate{store, func(id int64) {
		agents.Wake()
		jobChanged(id)
	}}, log)
	if err != nil {
		return fmt.Errorf("create webhook handler: %w", err)
	}

	broker := events.New(log)
	jobsAPI := api.NewJobsAPI(store, broker, log,
		api.WithWriteToken(cfg.APIToken),
		api.WithJobChanged(func(id int64) {
			agents.Wake() // dispatches reruns and delivers cancels here at once
			jobChanged(id)
		}))

	srv := &http.Server{
		Handler: api.NewRouter(api.Handlers{
			Webhook: webhook,
			Agents:  agents,
			Jobs:    jobsAPI,
			Secrets: api.NewSecretsAPI(vault, cfg.APIToken, log),
			DB:      store,
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

	// If one stops with an error, the others are shut down too.
	g, gctx := errgroup.WithContext(ctx)
	g.Go(func() error { return agents.Run(gctx) })
	if reporter != nil {
		g.Go(func() error { return reporter.Run(gctx) })
	}
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

// wakeOnCreate calls wake whenever the webhook queues a job, so it is
// dispatched and reported right away instead of on the next poll.
type wakeOnCreate struct {
	jobs api.JobCreator
	wake func(jobID int64)
}

func (w wakeOnCreate) CreateJob(ctx context.Context, job models.Job) (int64, error) {
	id, err := w.jobs.CreateJob(ctx, job)
	if err == nil {
		w.wake(id)
	}
	return id, err
}

// jobURL links commit statuses to the job when the public URL is known.
func jobURL(publicURL string) func(int64) string {
	if publicURL == "" {
		return nil
	}
	return func(id int64) string { return fmt.Sprintf("%s/api/jobs/%d", publicURL, id) }
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
