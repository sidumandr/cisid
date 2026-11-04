package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/runner"
)

func TestFirstToken(t *testing.T) {
	assert.Equal(t, "new-token", firstToken("new-token"))
	assert.Equal(t, "new-token", firstToken(" new-token , old-token"), "a rotation list sends the first")
	assert.Equal(t, "", firstToken(""))
}

func TestInsecureRemote(t *testing.T) {
	for url, want := range map[string]bool{
		"ws://ci.example.com/agent/ws":  true,
		"ws://10.0.0.5:8080/agent/ws":   true,
		"ws://localhost:8080/agent/ws":  false,
		"ws://127.0.0.1:8080/agent/ws":  false,
		"ws://[::1]:8080/agent/ws":      false,
		"wss://ci.example.com/agent/ws": false,
	} {
		assert.Equal(t, want, insecureRemote(url), url)
	}
}

func TestLimitsFromEnv(t *testing.T) {
	env := func(m map[string]string) func(string) string { return func(k string) string { return m[k] } }

	l, err := limitsFromEnv(env(nil))
	require.NoError(t, err)
	assert.Equal(t, runner.Limits{Pids: runner.DefaultPids}, l, "only the process limit by default")

	l, err = limitsFromEnv(env(map[string]string{"CISID_STEP_MEMORY": "2g", "CISID_STEP_CPUS": "1.5", "CISID_STEP_PIDS": "512"}))
	require.NoError(t, err)
	assert.Equal(t, runner.Limits{MemoryBytes: 2 << 30, NanoCPUs: 1_500_000_000, Pids: 512}, l)

	for k, v := range map[string]string{"CISID_STEP_MEMORY": "huge", "CISID_STEP_CPUS": "-1", "CISID_STEP_PIDS": "0"} {
		_, err := limitsFromEnv(env(map[string]string{k: v}))
		assert.ErrorContains(t, err, k)
	}
}

func TestSplitLabels(t *testing.T) {
	assert.Equal(t, []string{"arm64", "gpu"}, splitLabels(" arm64, gpu,,arm64 "))
	assert.Nil(t, splitLabels(""))
}
