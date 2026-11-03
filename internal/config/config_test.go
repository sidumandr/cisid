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
	}, cfg)
}

func TestLoadOverrides(t *testing.T) {
	cfg, err := Load(env(with(map[string]string{
		"CISID_HTTP_ADDR":        "127.0.0.1:9000",
		"CISID_SHUTDOWN_TIMEOUT": "45s",
		"CISID_LOG_LEVEL":        "debug",
	})))

	require.NoError(t, err)
	assert.Equal(t, "127.0.0.1:9000", cfg.HTTPAddr)
	assert.Equal(t, 45*time.Second, cfg.ShutdownTimeout)
	assert.Equal(t, slog.LevelDebug, cfg.LogLevel)
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
	cfg, err := Load(env(required))
	require.NoError(t, err)

	var buf bytes.Buffer
	slog.New(slog.NewTextHandler(&buf, nil)).Info("starting", "config", cfg)

	out := buf.String()
	assert.NotContains(t, out, "s3cret")
	assert.NotContains(t, out, "hook-secret")
	assert.Contains(t, out, "localhost:5432/cisid")
	assert.Contains(t, out, "config.http_addr=:8080")
}
