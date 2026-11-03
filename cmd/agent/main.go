// Command agent runs a cisid worker that executes pipeline jobs.
//
// Configuration (environment variables):
//
//	CISID_SERVER_URL  server agent endpoint (default ws://localhost:8080/agent/ws)
//	CISID_AGENT_NAME  name reported to the server (default: hostname)
//	DOCKER_HOST etc.  Docker daemon connection, see client.FromEnv
package main

import (
	"context"
	"fmt"
	"log/slog"
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

	cli, err := client.New(client.FromEnv)
	if err != nil {
		return fmt.Errorf("create docker client: %w", err)
	}
	defer cli.Close()

	a, err := agent.New(agent.Config{ServerURL: serverURL, Name: name},
		runner.NewDockerRunner(cli, runner.WithLogger(log)), log)
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
