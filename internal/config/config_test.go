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
	}, cfg)
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
			name: "missing required values are all reported",
			vars: map[string]string{},
			want: []string{"CISID_DATABASE_URL is required", "CISID_GITHUB_WEBHOOK_SECRET is required"},
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
