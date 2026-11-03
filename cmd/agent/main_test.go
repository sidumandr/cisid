package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
