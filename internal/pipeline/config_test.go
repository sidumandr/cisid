package pipeline

import (
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseConfig(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
# cisid pipeline
steps:
  - name: lint
    image: golang:1.27
    commands:
      - go vet ./...
    environment:
      CGO_ENABLED: 0
      GOFLAGS: -mod=readonly
    timeout: 5m
  - name: test
    image: golang:1.27
    commands:
      - go test ./...
      - echo done
`))

	require.NoError(t, err)
	assert.Equal(t, Config{Steps: []Step{
		{
			Name:        "lint",
			Image:       "golang:1.27",
			Commands:    []string{"go vet ./..."},
			Environment: map[string]string{"CGO_ENABLED": "0", "GOFLAGS": "-mod=readonly"},
			Timeout:     5 * time.Minute,
		},
		{
			Name:     "test",
			Image:    "golang:1.27",
			Commands: []string{"go test ./...", "echo done"},
			Timeout:  DefaultStepTimeout,
		},
	}}, cfg)
}

func TestParseConfigErrors(t *testing.T) {
	tests := []struct {
		name string
		yaml string
		want []string
	}{
		{name: "empty file", yaml: "", want: []string{"no steps"}},
		{name: "no steps", yaml: "steps: []", want: []string{"no steps"}},
		{name: "not yaml", yaml: "steps: [", want: []string{"parse"}},
		{name: "unknown field", yaml: "steps:\n  - name: a\n    image: x\n    command: [true]\n", want: []string{"command"}},
		{name: "multiple documents", yaml: "steps:\n  - {name: a, image: x, commands: [true]}\n---\nsteps: []\n", want: []string{"single YAML document"}},
		{
			name: "every step problem is reported",
			yaml: `
steps:
  - name: ""
    commands: [true]
  - name: build
    image: golang:1.27
  - name: build
    image: "golang 1.27"
    commands: ["", "true"]
    timeout: forever
  - name: clone
    image: alpine
    commands: [true]
    timeout: 48h
    environment:
      CISID_BRANCH: x
      "1BAD": y
`,
			want: []string{
				"steps[0]: name is required",
				"steps[0]: image is required",
				"steps[1]: commands are required",
				`steps[2]: name "build" is used twice`,
				`steps[2]: image "golang 1.27" contains whitespace`,
				"steps[2]: commands[0] is empty",
				`steps[2]: timeout "forever"`,
				`steps[3]: name "clone" is reserved`,
				"steps[3]: timeout 48h0m0s is over 24h0m0s",
				`steps[3]: environment "CISID_BRANCH" is reserved`,
				`steps[3]: environment name "1BAD" is invalid`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := ParseConfig([]byte(tt.yaml))

			require.ErrorIs(t, err, ErrInvalidConfig)
			for _, w := range tt.want {
				assert.Contains(t, err.Error(), w)
			}
		})
	}
}

func TestParseConfigLimits(t *testing.T) {
	_, err := ParseConfig([]byte("# " + strings.Repeat("x", maxConfigBytes)))
	assert.ErrorIs(t, err, ErrInvalidConfig)

	var b strings.Builder
	b.WriteString("steps:\n")
	for i := range maxSteps + 1 {
		b.WriteString("  - {name: s" + string(rune('a'+i%26)) + strings.Repeat("x", i/26) + ", image: alpine, commands: [true]}\n")
	}
	_, err = ParseConfig([]byte(b.String()))
	require.ErrorIs(t, err, ErrInvalidConfig)
	assert.Contains(t, err.Error(), "at most 50 steps")
}

func TestParseConfigWhenBranch(t *testing.T) {
	cfg, err := ParseConfig([]byte(`
steps:
  - name: test
    image: golang:1.27
    commands: [go test ./...]
  - name: deploy
    image: alpine:3.22
    commands: [./deploy.sh]
    when:
      branch: main
  - name: release
    image: alpine:3.22
    commands: [./release.sh]
    when:
      branch: [main, "release/*", "hotfix/**"]
`))
	require.NoError(t, err)

	runs := func(step int, branch string) bool { return cfg.Steps[step].RunsOn(branch) }
	assert.True(t, runs(0, "anything/at/all"), "no when: every branch")
	assert.True(t, runs(1, "main"))
	assert.False(t, runs(1, "mainline"), "patterns match the whole name")
	assert.True(t, runs(2, "release/1.0"))
	assert.False(t, runs(2, "release/1.0/rc1"), "* stays within a segment")
	assert.True(t, runs(2, "hotfix/a/b"), "** crosses segments")
	assert.False(t, runs(2, "feature/x"))
}

func TestParseConfigWhenErrors(t *testing.T) {
	for name, yml := range map[string]string{
		"unknown condition": "steps:\n  - {name: a, image: x, commands: [true], when: {event: push}}\n",
		"empty pattern":     "steps:\n  - {name: a, image: x, commands: [true], when: {branch: [\"\"]}}\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(yml))
			assert.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
}

func TestBranchGlobs(t *testing.T) {
	tests := []struct {
		pattern, branch string
		want            bool
	}{
		{"main", "main", true},
		{"feature/*", "feature/login", true},
		{"feature/*", "feature/a/b", false},
		{"feature/**", "feature/a/b", true},
		{"v?.x", "v1.x", true},
		{"v?.x", "v10.x", false},
		{"a.b", "axb", false},
		{"*", "main", true},
		{"*", "feature/x", false},
		{"**", "feature/x", true},
	}
	for _, tt := range tests {
		m, err := newBranchMatcher([]string{tt.pattern})
		require.NoError(t, err)
		assert.Equal(t, tt.want, m.Matches(tt.branch), "%q vs %q", tt.pattern, tt.branch)
	}
}

func TestParseConfigSecrets(t *testing.T) {
	cfg, err := ParseConfig([]byte("steps:\n  - {name: a, image: x, commands: [true], secrets: [TOKEN, KEY]}\n"))
	require.NoError(t, err)
	assert.Equal(t, []string{"TOKEN", "KEY"}, cfg.Steps[0].Secrets)

	_, err = ParseConfig([]byte(`
steps:
  - name: a
    image: x
    commands: [true]
    secrets: [BAD-NAME, CISID_X, TOKEN, TOKEN, GOFLAGS]
    environment:
      GOFLAGS: -mod=mod
`))
	require.ErrorIs(t, err, ErrInvalidConfig)
	for _, want := range []string{"BAD-NAME", `"CISID_X" is reserved`, `"TOKEN" is listed twice`, `"GOFLAGS" is also set in environment`} {
		assert.Contains(t, err.Error(), want)
	}
}
