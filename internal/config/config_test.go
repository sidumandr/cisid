package config

import (
	"bytes"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func env(vars map[string]string) func(string) string {
	return func(k string) string { return vars[k] }
}

var required = map[string]string{
	"CISID_DATABASE_URL":          "postgres://cisid:s3cret@localhost:5432/cisid?sslmode=disable",
	"CISID_GITHUB_WEBHOOK_SECRET": "hook-secret",
}

func with(extra map[string]string) map[string]string {
	m := map[string]string{}
	for k, v := range required {
		m[k] = v
	}
	for k, v := range extra {
		m[k] = v
	}
	return m
}

func TestLoadDefaults(t *testing.T) {
	cfg, err := Load(env(required))

	require.NoError(t, err)
	assert.Equal(t, Config{
		HTTPAddr:        ":8080",
		DatabaseURL:     required["CISID_DATABASE_URL"],
		WebhookSecret:   "hook-secret",
		ShutdownTimeout: 15 * time.Second,
		LogLevel:        slog.LevelInfo,
		GitHubAPIURL:    "https://api.github.com",
		RateLimit:       10,
		LogRetention:    90 * 24 * time.Hour,
		GitLab:          Forge{URL: "https://gitlab.com"},
		Bitbucket:       Forge{URL: "https://api.bitbucket.org/2.0"},
	}, cfg)
}

func TestLoadWithoutWebhooks(t *testing.T) {
	cfg, err := Load(env(map[string]string{"CISID_DATABASE_URL": required["CISID_DATABASE_URL"]}))
	require.NoError(t, err, "a server that only polls or builds by hand needs no webhook secret")
	assert.Empty(t, cfg.WebhookSecret)
}

func TestLoadForges(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{
		"CISID_GITEA_URL":                "https://git.example.com/",
		"CISID_GITEA_TOKEN":              "gitea-token",
		"CISID_GITEA_WEBHOOK_SECRET":     "gitea-hook",
		"CISID_GITLAB_URL":               "https://gitlab.example.com",
		"CISID_GITLAB_TOKEN":             "glpat-token",
		"CISID_BITBUCKET_WEBHOOK_SECRET": "bb-hook",
	})))
	require.NoError(t, err)
	assert.Equal(t, Forge{URL: "https://git.example.com", Token: "gitea-token", WebhookSecret: "gitea-hook"}, cfg.Gitea)
	assert.False(t, cfg.Forgejo.Enabled(), "Forgejo is off without its URL")
	assert.Equal(t, Forge{URL: "https://gitlab.example.com", Token: "glpat-token"}, cfg.GitLab)
	assert.Equal(t, Forge{URL: "https://api.bitbucket.org/2.0", WebhookSecret: "bb-hook"}, cfg.Bitbucket)

	_, err = Load(env(with(map[string]string{"CISID_FORGEJO_TOKEN": "t"})))
	assert.ErrorContains(t, err, "CISID_FORGEJO_URL is required")
	_, err = Load(env(with(map[string]string{"CISID_GITEA_URL": "git.example.com"})))
	assert.ErrorContains(t, err, "CISID_GITEA_URL must be an http(s) URL")
}

func TestLoadTokens(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{
		"CISID_AGENT_TOKEN": "new-agent-token-0123, old-agent-token-0123",
		"CISID_READ_TOKEN":  "read-token-0123456789",
	})))
	require.NoError(t, err)
	assert.Equal(t, []string{"new-agent-token-0123", "old-agent-token-0123"}, cfg.AgentTokens, "rotation: both are accepted")
	assert.Equal(t, "read-token-0123456789", cfg.ReadToken)

	for name, vars := range map[string]map[string]string{
		"short second agent token": {"CISID_AGENT_TOKEN": "new-agent-token-0123,old"},
		"short read token":         {"CISID_READ_TOKEN": "short"},
	} {
		_, err := Load(env(with(vars)))
		assert.ErrorIs(t, err, ErrInvalidConfig, name)
	}
}

func TestLoadBuildForks(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{"CISID_BUILD_FORKS": "true"})))
	require.NoError(t, err)
	assert.True(t, cfg.BuildForks)

	_, err = Load(env(with(map[string]string{"CISID_BUILD_FORKS": "sometimes"})))
	assert.ErrorIs(t, err, ErrInvalidConfig)
}

func TestLoadLogRetention(t *testing.T) {
	for v, want := range map[string]time.Duration{
		"30d": 30 * 24 * time.Hour,
		"36h": 36 * time.Hour,
		"0":   0,
		"0d":  0,
	} {
		cfg, err := Load(env(with(map[string]string{"CISID_LOG_RETENTION": v})))
		require.NoError(t, err, v)
		assert.Equal(t, want, cfg.LogRetention, v)
	}
	for _, v := range []string{"-1d", "-5h", "ninety", "1.5d"} {
		_, err := Load(env(with(map[string]string{"CISID_LOG_RETENTION": v})))
		assert.ErrorIs(t, err, ErrInvalidConfig, v)
	}
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{
		"CISID_HTTP_ADDR":        "127.0.0.1:9000",
		"CISID_SHUTDOWN_TIMEOUT": "45s",
		"CISID_LOG_LEVEL":        "debug",
		"CISID_GITHUB_API_URL":   "https://ghe.example.com/api/v3",
		"CISID_GITHUB_TOKEN":     "ghp_secret",
		"CISID_PUBLIC_URL":       "https://ci.example.com/",
	})))

	require.NoError(t, err)
	assert.Equal(t, "https://ci.example.com", cfg.PublicURL, "trailing slash is dropped")
	assert.Equal(t, "127.0.0.1:9000", cfg.HTTPAddr)
	assert.Equal(t, 45*time.Second, cfg.ShutdownTimeout)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
	assert.Equal(t, "https://ghe.example.com/api/v3", cfg.GitHubAPIURL)
	assert.Equal(t, "ghp_secret", cfg.GitHubToken)
}

func TestLoadErrors(t *testing.T) {
	tests := []struct {
		name string
		vars map[string]string
		want []string
	}{
		{
			name: "every problem is reported",
			vars: map[string]string{"CISID_GITLAB_URL": "gitlab"},
			want: []string{"CISID_DATABASE_URL is required", "CISID_GITLAB_URL must be an http(s) URL"},
		},
		{
			name: "bad duration",
			vars: with(map[string]string{"CISID_SHUTDOWN_TIMEOUT": "soon"}),
			want: []string{"CISID_SHUTDOWN_TIMEOUT"},
		},
		{
			name: "non-positive duration",
			vars: with(map[string]string{"CISID_SHUTDOWN_TIMEOUT": "0s"}),
			want: []string{"CISID_SHUTDOWN_TIMEOUT must be positive"},
		},
		{
			name: "bad log level",
			vars: with(map[string]string{"CISID_LOG_LEVEL": "loud"}),
			want: []string{"CISID_LOG_LEVEL"},
		},
		{
			name: "GitHub API URL is not http",
			vars: with(map[string]string{"CISID_GITHUB_API_URL": "ftp://github.example.com"}),
			want: []string{"CISID_GITHUB_API_URL must be an http(s) URL"},
		},
		{
			name: "bad secret key",
			vars: with(map[string]string{"CISID_SECRET_KEY": "c2hvcnQ="}),
			want: []string{"CISID_SECRET_KEY", "invalid secret key"},
		},
		{
			name: "bad rate limit",
			vars: with(map[string]string{"CISID_RATE_LIMIT": "-1", "CISID_TRUST_PROXY": "maybe"}),
			want: []string{"CISID_RATE_LIMIT must not be negative", "CISID_TRUST_PROXY"},
		},
		{
			name: "agent token too short",
			vars: with(map[string]string{"CISID_AGENT_TOKEN": "short"}),
			want: []string{"CISID_AGENT_TOKEN must be at least 16 characters"},
		},
		{
			name: "TLS key without certificate",
			vars: with(map[string]string{"CISID_TLS_KEY_FILE": "key.pem"}),
			want: []string{"must be set together"},
		},
		{
			name: "API token too short",
			vars: with(map[string]string{"CISID_API_TOKEN": "short"}),
			want: []string{"CISID_API_TOKEN must be at least 16 characters"},
		},
		{
			name: "public URL is not http",
			vars: with(map[string]string{"CISID_PUBLIC_URL": "ci.example.com"}),
			want: []string{"CISID_PUBLIC_URL must be an http(s) URL"},
		},
		{
			name: "database URL is not postgres",
			vars: with(map[string]string{"CISID_DATABASE_URL": "mysql://localhost/cisid"}),
			want: []string{"CISID_DATABASE_URL must be a postgres:// URL"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Load(env(tt.vars))

			require.ErrorIs(t, err, ErrInvalidConfig)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestConfigLogValueRedactsSecrets(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{"CISID_GITHUB_TOKEN": "ghp_secret", "CISID_API_TOKEN": "api-secret-0123456789"})))
	require.NoError(t, err)

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("starting", "config", cfg)

	out := buf.String()
	assert.NotContains(t, out, "s3cret")
	assert.NotContains(t, out, "hook-secret")
	assert.NotContains(t, out, "ghp_secret")
	assert.NotContains(t, out, "api-secret")
	assert.Contains(t, out, "config.api_token_set=true")
	assert.Contains(t, out, "config.secret_key_set=false")
	assert.Contains(t, out, "config.github_token_set=true")
	assert.Contains(t, out, "localhost:5432/cisid")
	assert.Contains(t, out, "config.http_addr=:8080")
}
