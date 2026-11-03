// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/url"
	"strconv"
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
	WebhookSecret   string        // CISID_GITHUB_WEBHOOK_SECRET, optional; enables /webhooks/github
	ShutdownTimeout time.Duration // CISID_SHUTDOWN_TIMEOUT, default 15s
	LogLevel        slog.Level    // CISID_LOG_LEVEL, default info
	GitHubAPIURL    string        // CISID_GITHUB_API_URL, default https://api.github.com
	GitHubToken     string        // CISID_GITHUB_TOKEN, optional (private repos, rate limits, commit statuses)
	PublicURL       string        // CISID_PUBLIC_URL, optional base URL users reach the server at
	APIToken        string        // CISID_API_TOKEN, optional; enables cancel/rerun and secrets
	SecretKey       []byte        // CISID_SECRET_KEY, optional base64 32-byte key; enables secrets
	AgentTokens     []string      // CISID_AGENT_TOKEN, optional comma-separated tokens agents must present; agents send the first
	ReadToken       string        // CISID_READ_TOKEN, optional; reading jobs, logs and metrics then needs it or the API token
	TLSCertFile     string        // CISID_TLS_CERT_FILE, optional; serve HTTPS with TLSKeyFile
	TLSKeyFile      string        // CISID_TLS_KEY_FILE
	RateLimit       float64       // CISID_RATE_LIMIT, requests per second per client, default 10, 0 disables
	TrustProxy      bool          // CISID_TRUST_PROXY, take the client from X-Forwarded-For
	LogRetention    time.Duration // CISID_LOG_RETENTION, how long finished jobs keep their logs, default 90 days, 0 forever
	BuildForks      bool          // CISID_BUILD_FORKS, build pull requests from forks (without secrets)

	// The other forges. Gitea and Forgejo are on when their URL is set;
	// GitLab (GitLab.com by default) and Bitbucket Cloud always are, as
	// GitHub is. A webhook secret enables the forge's /webhooks/{forge}.
	Gitea     Forge // CISID_GITEA_URL, CISID_GITEA_TOKEN, CISID_GITEA_WEBHOOK_SECRET
	Forgejo   Forge // CISID_FORGEJO_URL, CISID_FORGEJO_TOKEN, CISID_FORGEJO_WEBHOOK_SECRET
	GitLab    Forge // CISID_GITLAB_URL (default https://gitlab.com), CISID_GITLAB_TOKEN, CISID_GITLAB_WEBHOOK_SECRET
	Bitbucket Forge // CISID_BITBUCKET_API_URL (default https://api.bitbucket.org/2.0), CISID_BITBUCKET_TOKEN, CISID_BITBUCKET_WEBHOOK_SECRET
}

// Forge is how the server reaches one forge.
type Forge struct {
	URL           string // web URL (Gitea, Forgejo, GitLab) or API URL (Bitbucket)
	Token         string // access token: private repositories, rate limits, commit statuses
	WebhookSecret string
}

// Enabled reports whether the forge is configured.
func (f Forge) Enabled() bool { return f.URL != "" }

func (f Forge) logValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", f.URL),
		slog.Bool("token_set", f.Token != ""),
		slog.Bool("webhook", f.WebhookSecret != ""),
	)
}

// loadForge reads the CISID_<prefix>_* settings of a forge. A forge with a
// default URL is always on; one without needs its URL.
func loadForge(getenv func(string) string, prefix, defaultURL string, problems *[]error) Forge {
	f := Forge{
		URL:           getenv("CISID_" + prefix + "_URL"),
		Token:         getenv("CISID_" + prefix + "_TOKEN"),
		WebhookSecret: getenv("CISID_" + prefix + "_WEBHOOK_SECRET"),
	}
	urlVar := "CISID_" + prefix + "_URL"
	if prefix == "BITBUCKET" {
		f.URL = getenv("CISID_BITBUCKET_API_URL")
		urlVar = "CISID_BITBUCKET_API_URL"
	}
	switch {
	case f.URL == "" && defaultURL != "":
		f.URL = defaultURL
	case f.URL == "":
		if f.Token != "" || f.WebhookSecret != "" {
			*problems = append(*problems, fmt.Errorf("%s is required with CISID_%s_TOKEN or CISID_%s_WEBHOOK_SECRET", urlVar, prefix, prefix))
		}
	default:
		if u, err := url.Parse(f.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			*problems = append(*problems, fmt.Errorf("%s must be an http(s) URL", urlVar))
		}
		f.URL = strings.TrimSuffix(f.URL, "/")
	}
	return f
}

// defaultLogRetention matches GitHub Actions' default.
const defaultLogRetention = 90 * 24 * time.Hour

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
		ReadToken:     getenv("CISID_READ_TOKEN"),
		TLSCertFile:   getenv("CISID_TLS_CERT_FILE"),
		TLSKeyFile:    getenv("CISID_TLS_KEY_FILE"),
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

	if cfg.GitHubAPIURL == "" {
		cfg.GitHubAPIURL = "https://api.github.com"
	} else if u, err := url.Parse(cfg.GitHubAPIURL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		problems = append(problems, fmt.Errorf("CISID_GITHUB_API_URL must be an http(s) URL"))
	}

	cfg.Gitea = loadForge(getenv, "GITEA", "", &problems)
	cfg.Forgejo = loadForge(getenv, "FORGEJO", "", &problems)
	cfg.GitLab = loadForge(getenv, "GITLAB", "https://gitlab.com", &problems)
	cfg.Bitbucket = loadForge(getenv, "BITBUCKET", "https://api.bitbucket.org/2.0", &problems)

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

	cfg.RateLimit = 10
	if v := getenv("CISID_RATE_LIMIT"); v != "" {
		n, err := strconv.ParseFloat(v, 64)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("CISID_RATE_LIMIT: %w", err))
		case n < 0:
			problems = append(problems, fmt.Errorf("CISID_RATE_LIMIT must not be negative"))
		default:
			cfg.RateLimit = n
		}
	}
	if v := getenv("CISID_TRUST_PROXY"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_TRUST_PROXY: %w", err))
		}
		cfg.TrustProxy = b
	}

	// Several tokens let agents move to a new one before the old one goes.
	for _, t := range strings.Split(getenv("CISID_AGENT_TOKEN"), ",") {
		if t = strings.TrimSpace(t); t != "" {
			cfg.AgentTokens = append(cfg.AgentTokens, t)
		}
	}
	for _, t := range cfg.AgentTokens {
		if len(t) < minAPITokenLen {
			problems = append(problems, fmt.Errorf("CISID_AGENT_TOKEN must be at least %d characters", minAPITokenLen))
			break
		}
	}
	if cfg.ReadToken != "" && len(cfg.ReadToken) < minAPITokenLen {
		problems = append(problems, fmt.Errorf("CISID_READ_TOKEN must be at least %d characters", minAPITokenLen))
	}
	if (cfg.TLSCertFile == "") != (cfg.TLSKeyFile == "") {
		problems = append(problems, fmt.Errorf("CISID_TLS_CERT_FILE and CISID_TLS_KEY_FILE must be set together"))
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

	if v := getenv("CISID_BUILD_FORKS"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_BUILD_FORKS: %w", err))
		}
		cfg.BuildForks = b
	}

	cfg.LogRetention = defaultLogRetention
	if v := getenv("CISID_LOG_RETENTION"); v != "" {
		d, err := parseRetention(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_LOG_RETENTION: %w", err))
		}
		cfg.LogRetention = d
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

// parseRetention reads a duration such as "90d" or "36h"; "0" means forever.
func parseRetention(v string) (time.Duration, error) {
	if days, ok := strings.CutSuffix(v, "d"); ok {
		n, err := strconv.Atoi(days)
		if err != nil || n < 0 {
			return 0, fmt.Errorf("%q is not a number of days", v)
		}
		return time.Duration(n) * 24 * time.Hour, nil
	}
	if v == "0" {
		return 0, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("parse %q: %w", v, err)
	}
	if d < 0 {
		return 0, fmt.Errorf("%q must not be negative", v)
	}
	return d, nil
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
		slog.Bool("github_webhook", c.WebhookSecret != ""),
		slog.Any("gitea", c.Gitea.logValue()),
		slog.Any("forgejo", c.Forgejo.logValue()),
		slog.Any("gitlab", c.GitLab.logValue()),
		slog.Any("bitbucket", c.Bitbucket.logValue()),
		slog.String("public_url", c.PublicURL),
		slog.Bool("api_token_set", c.APIToken != ""),
		slog.Bool("secret_key_set", c.SecretKey != nil),
		slog.Int("agent_tokens", len(c.AgentTokens)),
		slog.Bool("read_token_set", c.ReadToken != ""),
		slog.Bool("tls", c.TLSCertFile != ""),
		slog.Float64("rate_limit", c.RateLimit),
		slog.Bool("trust_proxy", c.TrustProxy),
		slog.Duration("log_retention", c.LogRetention),
		slog.Bool("build_forks", c.BuildForks),
	)
}
