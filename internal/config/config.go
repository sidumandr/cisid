// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/secrets"
)

// minAPITokenLen keeps API tokens out of guessing range.
const minAPITokenLen = 16

// ErrInvalidConfig is returned when required settings are missing or invalid.
var ErrInvalidConfig = errors.New("invalid config")

// Config holds the server settings.
type Config struct {
	HTTPAddr        string        // CISID_HTTP_ADDR, default ":8080"
	DatabaseURL     string        // CISID_DATABASE_URL, required
	WebhookSecret   string        // CISID_GITHUB_WEBHOOK_SECRET, required
	ShutdownTimeout time.Duration // CISID_SHUTDOWN_TIMEOUT, default 15s
	LogLevel        slog.Level    // CISID_LOG_LEVEL, default info
	GitHubAPIURL    string        // CISID_GITHUB_API_URL, default https://api.github.com
	GitHubToken     string        // CISID_GITHUB_TOKEN, optional (private repos, rate limits, commit statuses)
	PublicURL       string        // CISID_PUBLIC_URL, optional base URL users reach the server at
	APIToken        string        // CISID_API_TOKEN, optional; enables cancel/rerun and secrets
	SecretKey       []byte        // CISID_SECRET_KEY, optional base64 32-byte key; enables secrets
}

// Load reads the configuration through getenv (normally os.Getenv) and
// reports every problem at once.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		HTTPAddr:      getenv("CISID_HTTP_ADDR"),
		DatabaseURL:   getenv("CISID_DATABASE_URL"),
		WebhookSecret: getenv("CISID_GITHUB_WEBHOOK_SECRET"),
		GitHubAPIURL:  getenv("CISID_GITHUB_API_URL"),
		GitHubToken:   getenv("CISID_GITHUB_TOKEN"),
		PublicURL:     strings.TrimSuffix(getenv("CISID_PUBLIC_URL"), "/"),
		APIToken:      getenv("CISID_API_TOKEN"),
	}
	var problems []error

	if cfg.HTTPAddr == "" {
		cfg.HTTPAddr = ":8080"
	}

	if cfg.DatabaseURL == "" {
		problems = append(problems, fmt.Errorf("CISID_DATABASE_URL is required"))
	} else if u, err := url.Parse(cfg.DatabaseURL); err != nil || (u.Scheme != "postgres" && u.Scheme != "postgresql") {
		problems = append(problems, fmt.Errorf("CISID_DATABASE_URL must be a postgres:// URL"))
	}

	if cfg.WebhookSecret == "" {
		problems = append(problems, fmt.Errorf("CISID_GITHUB_WEBHOOK_SECRET is required"))
	}

	if cfg.GitHubAPIURL == "" {
		cfg.GitHubAPIURL = "https://api.github.com"
	} else if u, err := url.Parse(cfg.GitHubAPIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		problems = append(problems, fmt.Errorf("CISID_GITHUB_API_URL must be an http(s) URL"))
	}

	if cfg.PublicURL != "" {
		if u, err := url.Parse(cfg.PublicURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			problems = append(problems, fmt.Errorf("CISID_PUBLIC_URL must be an http(s) URL"))
		}
	}

	if v := getenv("CISID_SECRET_KEY"); v != "" {
		key, err := secrets.ParseKey(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_SECRET_KEY: %w", err))
		}
		cfg.SecretKey = key
	}

	if cfg.APIToken != "" && len(cfg.APIToken) < minAPITokenLen {
		problems = append(problems, fmt.Errorf("CISID_API_TOKEN must be at least %d characters", minAPITokenLen))
	}

	cfg.ShutdownTimeout = 15 * time.Second
	if v := getenv("CISID_SHUTDOWN_TIMEOUT"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("CISID_SHUTDOWN_TIMEOUT: %w", err))
		case d <= 0:
			problems = append(problems, fmt.Errorf("CISID_SHUTDOWN_TIMEOUT must be positive"))
		default:
			cfg.ShutdownTimeout = d
		}
	}

	if v := getenv("CISID_LOG_LEVEL"); v != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(v)); err != nil {
			problems = append(problems, fmt.Errorf("CISID_LOG_LEVEL: %w", err))
		}
	}

	if len(problems) > 0 {
		return Config{}, fmt.Errorf("%w: %w", ErrInvalidConfig, errors.Join(problems...))
	}
	return cfg, nil
}

// LogValue implements slog.LogValuer so secrets never reach the logs.
func (c Config) LogValue() slog.Value {
	dbURL := "invalid"
	if u, err := url.Parse(c.DatabaseURL); err == nil {
		dbURL = u.Redacted()
	}
	return slog.GroupValue(
		slog.String("http_addr", c.HTTPAddr),
		slog.String("database_url", dbURL),
		slog.Duration("shutdown_timeout", c.ShutdownTimeout),
		slog.String("log_level", c.LogLevel.String()),
		slog.String("github_api_url", c.GitHubAPIURL),
		slog.Bool("github_token_set", c.GitHubToken != ""),
		slog.String("public_url", c.PublicURL),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.Bool("secret_key_set", c.SecretKey != nil),
	)
}
