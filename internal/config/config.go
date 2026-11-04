// Package config loads server configuration from environment variables.
package config

import (
	"errors"
	"fmt"
	"log/slog"
	"net/mail"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/sidumandr/cisid/internal/auth"
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
	BuildForks      bool          // CISID_BUILD_FORKS, build pull requests from forks (without secrets): true, false or approve
	ApproveForks    bool          // CISID_BUILD_FORKS=approve: a maintainer approves each fork job first
	PollInterval    time.Duration // CISID_POLL_INTERVAL, how often watched branches are polled, default 1m, at least 10s, 0 turns polling off
	PublicRead      bool          // CISID_READ_ACCESS: "signed-in" (default) needs a sign-in or token to read, "public" lets anyone read
	SessionTTL      time.Duration // CISID_SESSION_TTL, how long a sign-in lasts, default 7d
	AdminUser       string        // CISID_ADMIN_USER, default "admin": the account CISID_ADMIN_PASSWORD creates
	AdminPassword   string        // CISID_ADMIN_PASSWORD, optional: creates AdminUser as an admin if it does not exist
	GitHubOAuth     OAuthApp      // CISID_GITHUB_OAUTH_CLIENT_ID, CISID_GITHUB_OAUTH_CLIENT_SECRET: sign in through GitHub
	OAuthSignupRole string        // CISID_OAUTH_SIGNUP_ROLE, viewer or maintainer: forge users without an account get one; empty: only linked users sign in
	SignupRole      string        // CISID_SIGNUP_ROLE, viewer or maintainer: anyone may open an account with a password; empty: only the first account (an admin) and admins create accounts
	PublicBadges    bool          // CISID_PUBLIC_BADGES, lets anyone see build badges even when reading needs a sign-in
	ArtifactMaxSize int64         // CISID_ARTIFACT_MAX_SIZE, the largest artifact accepted, default 100MB
	SMTP            SMTP          // CISID_SMTP_*: the relay notifications are emailed through

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
	OAuth         OAuthApp // CISID_<FORGE>_OAUTH_CLIENT_ID and _SECRET: sign in through the forge
}

// OAuthApp is an OAuth application registered on a forge.
type OAuthApp struct {
	ClientID     string
	ClientSecret string
}

// Enabled reports whether users can sign in through the app.
func (o OAuthApp) Enabled() bool { return o.ClientID != "" }

// loadOAuth reads CISID_<prefix>_OAUTH_CLIENT_ID and _SECRET, which go
// together.
func loadOAuth(getenv func(string) string, prefix string, problems *[]error) OAuthApp {
	o := OAuthApp{
		ClientID:     getenv("CISID_" + prefix + "_OAUTH_CLIENT_ID"),
		ClientSecret: getenv("CISID_" + prefix + "_OAUTH_CLIENT_SECRET"),
	}
	if (o.ClientID == "") != (o.ClientSecret == "") {
		*problems = append(*problems, fmt.Errorf("CISID_%s_OAUTH_CLIENT_ID and CISID_%s_OAUTH_CLIENT_SECRET must be set together", prefix, prefix))
	}
	return o
}

// Enabled reports whether the forge is configured.
func (f Forge) Enabled() bool { return f.URL != "" }

// SMTP is the relay notifications are emailed through.
type SMTP struct {
	Host     string // CISID_SMTP_HOST; empty turns email off
	Port     int    // CISID_SMTP_PORT, default 587, or 465 with TLS "tls"
	Username string // CISID_SMTP_USERNAME, optional, with Password
	Password string // CISID_SMTP_PASSWORD
	From     string // CISID_SMTP_FROM, required with Host, e.g. "cisid <ci@example.com>"
	TLS      string // CISID_SMTP_TLS: starttls (default), tls or none
}

// Enabled reports whether email can be sent.
func (s SMTP) Enabled() bool { return s.Host != "" }

func (s SMTP) logValue() slog.Value {
	return slog.GroupValue(
		slog.String("host", s.Host),
		slog.Int("port", s.Port),
		slog.String("tls", s.TLS),
		slog.String("from", s.From),
		slog.Bool("auth", s.Username != ""),
	)
}

// loadSMTP reads the CISID_SMTP_* settings, which need CISID_SMTP_HOST.
func loadSMTP(getenv func(string) string, problems *[]error) SMTP {
	s := SMTP{
		Host:     getenv("CISID_SMTP_HOST"),
		Username: getenv("CISID_SMTP_USERNAME"),
		Password: getenv("CISID_SMTP_PASSWORD"),
		From:     getenv("CISID_SMTP_FROM"),
		TLS:      getenv("CISID_SMTP_TLS"),
	}
	port := getenv("CISID_SMTP_PORT")
	if s.Host == "" {
		if s.Username != "" || s.Password != "" || s.From != "" || s.TLS != "" || port != "" {
			*problems = append(*problems, fmt.Errorf("CISID_SMTP_HOST is required with the other CISID_SMTP_ settings"))
		}
		return s
	}
	switch s.TLS {
	case "":
		s.TLS = "starttls"
	case "starttls", "tls", "none":
	default:
		*problems = append(*problems, fmt.Errorf("CISID_SMTP_TLS must be starttls, tls or none"))
	}
	s.Port = 587
	if s.TLS == "tls" {
		s.Port = 465
	}
	if port != "" {
		n, err := strconv.Atoi(port)
		if err != nil || n < 1 || n > 65535 {
			*problems = append(*problems, fmt.Errorf("CISID_SMTP_PORT must be a port number"))
		}
		s.Port = n
	}
	if s.From == "" {
		*problems = append(*problems, fmt.Errorf("CISID_SMTP_FROM is required with CISID_SMTP_HOST"))
	} else if _, err := mail.ParseAddress(s.From); err != nil {
		*problems = append(*problems, fmt.Errorf("CISID_SMTP_FROM must be an email address"))
	}
	if (s.Username == "") != (s.Password == "") {
		*problems = append(*problems, fmt.Errorf("CISID_SMTP_USERNAME and CISID_SMTP_PASSWORD must be set together"))
	}
	return s
}

func (f Forge) logValue() slog.Value {
	return slog.GroupValue(
		slog.String("url", f.URL),
		slog.Bool("token_set", f.Token != ""),
		slog.Bool("webhook", f.WebhookSecret != ""),
		slog.Bool("sign_in", f.OAuth.Enabled()),
	)
}

// loadForge reads the CISID_<prefix>_* settings of a forge. A forge with a
// default URL is always on; one without needs its URL.
func loadForge(getenv func(string) string, prefix, defaultURL string, problems *[]error) Forge {
	f := Forge{
		URL:           getenv("CISID_" + prefix + "_URL"),
		Token:         getenv("CISID_" + prefix + "_TOKEN"),
		WebhookSecret: getenv("CISID_" + prefix + "_WEBHOOK_SECRET"),
		OAuth:         loadOAuth(getenv, prefix, problems),
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
		if f.Token != "" || f.WebhookSecret != "" || f.OAuth.Enabled() {
			*problems = append(*problems, fmt.Errorf("%s is required with the forge's token, webhook secret or OAuth app", urlVar))
		}
	default:
		if u, err := url.Parse(f.URL); err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
			*problems = append(*problems, fmt.Errorf("%s must be an http(s) URL", urlVar))
		}
		f.URL = strings.TrimSuffix(f.URL, "/")
	}
	return f
}

// Polling bounds; see internal/trigger.
const (
	defaultPollInterval = time.Minute
	minPollInterval     = 10 * time.Second
)

// defaultSessionTTL is how long a sign-in lasts.
const defaultSessionTTL = 7 * 24 * time.Hour

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

	if v := getenv("CISID_BUILD_FORKS"); v == "approve" {
		cfg.BuildForks, cfg.ApproveForks = true, true
	} else if v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_BUILD_FORKS must be true, false or approve"))
		}
		cfg.BuildForks = b
	}

	cfg.PollInterval = defaultPollInterval
	if v := getenv("CISID_POLL_INTERVAL"); v != "" {
		d, err := time.ParseDuration(v)
		switch {
		case v == "0":
			cfg.PollInterval = 0
		case err != nil:
			problems = append(problems, fmt.Errorf("CISID_POLL_INTERVAL: %w", err))
		case d < minPollInterval:
			problems = append(problems, fmt.Errorf("CISID_POLL_INTERVAL must be at least %s, or 0 to turn polling off", minPollInterval))
		default:
			cfg.PollInterval = d
		}
	}

	// Builds run whoever is watching; seeing them, their logs included,
	// takes a sign-in or a token unless the server says otherwise.
	switch v := getenv("CISID_READ_ACCESS"); v {
	case "", "signed-in":
		cfg.PublicRead = false
	case "public":
		if cfg.ReadToken != "" {
			problems = append(problems, fmt.Errorf("CISID_READ_ACCESS=public and CISID_READ_TOKEN contradict each other"))
		}
		cfg.PublicRead = true
	default:
		problems = append(problems, fmt.Errorf("CISID_READ_ACCESS must be public or signed-in"))
	}

	cfg.SessionTTL = defaultSessionTTL
	if v := getenv("CISID_SESSION_TTL"); v != "" {
		d, err := parseRetention(v)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("CISID_SESSION_TTL: %w", err))
		case d < time.Minute:
			problems = append(problems, fmt.Errorf("CISID_SESSION_TTL must be at least 1m"))
		default:
			cfg.SessionTTL = d
		}
	}

	cfg.AdminUser = getenv("CISID_ADMIN_USER")
	if cfg.AdminUser == "" {
		cfg.AdminUser = "admin"
	} else if !auth.ValidUsername(cfg.AdminUser) {
		problems = append(problems, fmt.Errorf("CISID_ADMIN_USER may only contain a-z, 0-9, '.', '_' and '-'"))
	}
	cfg.AdminPassword = getenv("CISID_ADMIN_PASSWORD")
	if cfg.AdminPassword != "" && len(cfg.AdminPassword) < auth.MinPasswordLen {
		problems = append(problems, fmt.Errorf("CISID_ADMIN_PASSWORD must be at least %d characters", auth.MinPasswordLen))
	}

	cfg.GitHubOAuth = loadOAuth(getenv, "GITHUB", &problems)
	cfg.SignupRole = getenv("CISID_SIGNUP_ROLE")
	if r := cfg.SignupRole; r != "" && r != "viewer" && r != "maintainer" {
		problems = append(problems, fmt.Errorf("CISID_SIGNUP_ROLE must be viewer or maintainer"))
	}
	cfg.OAuthSignupRole = getenv("CISID_OAUTH_SIGNUP_ROLE")
	if r := cfg.OAuthSignupRole; r != "" && r != "viewer" && r != "maintainer" {
		problems = append(problems, fmt.Errorf("CISID_OAUTH_SIGNUP_ROLE must be viewer or maintainer"))
	}
	if cfg.PublicURL == "" && (cfg.GitHubOAuth.Enabled() || cfg.Gitea.OAuth.Enabled() || cfg.Forgejo.OAuth.Enabled() ||
		cfg.GitLab.OAuth.Enabled() || cfg.Bitbucket.OAuth.Enabled()) {
		problems = append(problems, fmt.Errorf("CISID_PUBLIC_URL is required for signing in through a forge: the forge sends the browser back there"))
	}

	if v := getenv("CISID_PUBLIC_BADGES"); v != "" {
		b, err := strconv.ParseBool(v)
		if err != nil {
			problems = append(problems, fmt.Errorf("CISID_PUBLIC_BADGES: %w", err))
		}
		cfg.PublicBadges = b
	}
	cfg.SMTP = loadSMTP(getenv, &problems)

	cfg.ArtifactMaxSize = defaultArtifactMaxSize
	if v := getenv("CISID_ARTIFACT_MAX_SIZE"); v != "" {
		n, err := parseSize(v)
		switch {
		case err != nil:
			problems = append(problems, fmt.Errorf("CISID_ARTIFACT_MAX_SIZE: %w", err))
		case n < 1 || n > maxArtifactMaxSize:
			problems = append(problems, fmt.Errorf("CISID_ARTIFACT_MAX_SIZE must be between 1 byte and 10GB"))
		default:
			cfg.ArtifactMaxSize = n
		}
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

// Artifact size bounds.
const (
	defaultArtifactMaxSize = 100 << 20
	maxArtifactMaxSize     = 10 << 30
)

// parseSize reads a size such as "100MB", "512KB", "2GB" or a number of
// bytes; KB, MB and GB are powers of 1024.
func parseSize(v string) (int64, error) {
	units := []struct {
		suffix string
		shift  uint
	}{{"GB", 30}, {"MB", 20}, {"KB", 10}, {"B", 0}}
	num, shift := strings.TrimSpace(v), uint(0)
	for _, u := range units {
		if n, ok := strings.CutSuffix(strings.ToUpper(num), u.suffix); ok {
			num, shift = strings.TrimSpace(n), u.shift
			break
		}
	}
	n, err := strconv.ParseInt(num, 10, 64)
	if err != nil || n < 0 || n > (1<<62)>>shift {
		return 0, fmt.Errorf("%q is not a size such as 100MB", v)
	}
	return n << shift, nil
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
		slog.Bool("approve_forks", c.ApproveForks),
		slog.Duration("poll_interval", c.PollInterval),
		slog.Bool("public_read", c.PublicRead),
		slog.Duration("session_ttl", c.SessionTTL),
		slog.String("admin_user", c.AdminUser),
		slog.Bool("admin_password_set", c.AdminPassword != ""),
		slog.Bool("github_sign_in", c.GitHubOAuth.Enabled()),
		slog.String("oauth_signup_role", c.OAuthSignupRole),
		slog.String("signup_role", c.SignupRole),
		slog.Bool("public_badges", c.PublicBadges),
		slog.Int64("artifact_max_size", c.ArtifactMaxSize),
		slog.Any("smtp", c.SMTP.logValue()),
	)
}
