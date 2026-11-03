// Command agent runs a cisid worker that executes pipeline jobs.
//
// Configuration (environment variables):
//
//	CISID_SERVER_URL      server agent endpoint (default ws://localhost:8080/agent/ws)
//	CISID_AGENT_NAME      name reported to the server (default: hostname)
//	CISID_AGENT_TOKEN     token the server requires from agents (CISID_AGENT_TOKEN there)
//	CISID_SERVER_CA_FILE  PEM CA bundle to trust for wss://, e.g. a private CA
//	CISID_STEP_MEMORY     memory limit per step and service container, e.g. 2g
//	CISID_STEP_CPUS       CPU limit per container, e.g. 2 or 0.5
//	CISID_STEP_PIDS       process limit per container (default 4096)
//	OTEL_EXPORTER_OTLP_*  OTLP/HTTP trace export, see docs/tracing.md (off when unset)
//	DOCKER_HOST etc.      Docker daemon connection, see client.FromEnv
package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"github.com/moby/moby/client"

	"github.com/sidumandr/cisid/internal/agent"
	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/internal/telemetry"
)

const (
	defaultServerURL  = "ws://localhost:8080/agent/ws"
	traceFlushTimeout = 5 * time.Second
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(telemetry.LogHandler(slog.NewTextHandler(os.Stderr, nil)))
	if err := run(ctx, log); err != nil {
		log.Error("agent stopped", "error", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, log *slog.Logger) error {
	name := os.Getenv("CISID_AGENT_NAME")
	if name == "" {
		host, err := os.Hostname()
		if err != nil {
			return fmt.Errorf("determine agent name: %w", err)
		}
		name = host
	}
	serverURL := os.Getenv("CISID_SERVER_URL")
	if serverURL == "" {
		serverURL = defaultServerURL
	}

	cfg := agent.Config{ServerURL: serverURL, Name: name, Token: os.Getenv("CISID_AGENT_TOKEN")}
	if caFile := os.Getenv("CISID_SERVER_CA_FILE"); caFile != "" {
		pem, err := os.ReadFile(caFile)
		if err != nil {
			return fmt.Errorf("read CISID_SERVER_CA_FILE: %w", err)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(pem) {
			return fmt.Errorf("CISID_SERVER_CA_FILE %s holds no PEM certificates", caFile)
		}
		cfg.TLS = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
	}
	if cfg.Token != "" && insecureRemote(serverURL) {
		log.WarnContext(ctx, "sending the agent token over unencrypted ws://; use wss:// for remote servers")
	}

	limits, err := limitsFromEnv(os.Getenv)
	if err != nil {
		return err
	}

	tracing, err := telemetry.Setup(ctx, "cisid-agent", os.Getenv)
	if err != nil {
		return fmt.Errorf("set up tracing: %w", err)
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), traceFlushTimeout)
		defer cancel()
		if err := tracing.Shutdown(ctx); err != nil {
			log.WarnContext(ctx, "could not export all spans", "error", err)
		}
	}()
	if tracing.Enabled() {
		cfg.TracerProvider = tracing.Provider
		log.InfoContext(ctx, "tracing enabled", "endpoint", tracing.Endpoint)
	}

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	a, err := agent.New(cfg, runner.NewDockerRunner(cli, runner.WithLogger(log), runner.WithLimits(limits)), log)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	log.InfoContext(ctx, "agent starting", "name", name, "server", serverURL,
		"memory_limit", limits.MemoryBytes, "cpu_limit", float64(limits.NanoCPUs)/1e9, "pids_limit", limits.Pids)
	if err := a.Run(ctx); err != nil {
		return fmt.Errorf("run agent: %w", err)
	}
	log.InfoContext(ctx, "agent stopped")
	return nil
}

// insecureRemote reports whether u is a ws:// URL to a host other than
// this machine.
func insecureRemote(u string) bool {
	parsed, err := url.Parse(u)
	if err != nil || parsed.Scheme != "ws" {
		return false
	}
	host := parsed.Hostname()
	if host == "localhost" {
		return false
	}
	ip := net.ParseIP(host)
	return ip == nil || !ip.IsLoopback()
}

// limitsFromEnv reads the container limits.
func limitsFromEnv(getenv func(string) string) (runner.Limits, error) {
	l := runner.Limits{Pids: runner.DefaultPids}
	var err error
	if l.MemoryBytes, err = runner.ParseMemory(getenv("CISID_STEP_MEMORY")); err != nil {
		return l, fmt.Errorf("CISID_STEP_MEMORY: %w", err)
	}
	if l.NanoCPUs, err = runner.ParseCPUs(getenv("CISID_STEP_CPUS")); err != nil {
		return l, fmt.Errorf("CISID_STEP_CPUS: %w", err)
	}
	if v := getenv("CISID_STEP_PIDS"); v != "" {
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n <= 0 {
			return l, fmt.Errorf("CISID_STEP_PIDS: %q is not a positive number", v)
		}
		l.Pids = n
	}
	return l, nil
}
