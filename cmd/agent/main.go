// Command agent runs a cisid worker that executes pipeline jobs.
//
// Configuration (environment variables):
//
//	CISID_SERVER_URL      server agent endpoint (default ws://localhost:8080/agent/ws)
//	CISID_AGENT_NAME      name reported to the server (default: hostname)
//	CISID_AGENT_TOKEN     token the server requires from agents (CISID_AGENT_TOKEN there)
//	CISID_SERVER_CA_FILE  PEM CA bundle to trust for wss://, e.g. a private CA
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
	"syscall"

	"github.com/moby/moby/client"

	"github.com/sidumandr/cisid/internal/agent"
	"github.com/sidumandr/cisid/internal/runner"
)

const defaultServerURL = "ws://localhost:8080/agent/ws"

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	log := slog.New(slog.NewTextHandler(os.Stderr, nil))
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

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	a, err := agent.New(cfg, runner.NewDockerRunner(cli, runner.WithLogger(log)), log)
	if err != nil {
		return fmt.Errorf("create agent: %w", err)
	}

	log.InfoContext(ctx, "agent starting", "name", name, "server", serverURL)
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
