package pipeline

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

const cacheConfig = `
cache:
  - /go/pkg/mod
  - /root/.cache/go-build
steps:
  - {name: test, image: golang:1.27, commands: [go test ./...]}
`

func TestParseConfigCache(t *testing.T) {
	cfg, err := ParseConfig([]byte(cacheConfig))
	require.NoError(t, err)
	assert.Equal(t, []string{"/go/pkg/mod", "/root/.cache/go-build"}, cfg.Cache)

	steps := "steps:\n  - {name: a, image: x, commands: [true]}\n"
	for name, cache := range map[string]string{
		"relative":   "cache: [node_modules]\n",
		"unclean":    "cache: [/a/../b]\n",
		"root":       "cache: [/]\n",
		"workspace":  "cache: [/workspace]\n",
		"twice":      "cache: [/a, /a]\n",
		"too many":   "cache: [/1, /2, /3, /4, /5, /6, /7, /8, /9, /10, /11]\n",
		"not a list": "cache: /go/pkg/mod\n",
	} {
		t.Run(name, func(t *testing.T) {
			_, err := ParseConfig([]byte(cache + steps))
			assert.ErrorIs(t, err, ErrInvalidConfig)
		})
	}
	_, err = ParseConfig([]byte("cache: [/workspace/node_modules]\n" + steps))
	assert.NoError(t, err, "a directory inside the workspace may be cached")
}

func TestPlanCache(t *testing.T) {
	plan := func(job models.Job) *models.CacheSpec {
		p, err := Planner{Configs: &fakeConfigs{data: []byte(cacheConfig)}}.Plan(t.Context(), job)
		require.NoError(t, err)
		return p.Assignment.Cache
	}

	c := plan(testJob())
	require.NotNil(t, c)
	assert.Equal(t, []string{"/go/pkg/mod", "/root/.cache/go-build"}, c.Paths)
	assert.Len(t, c.Key, 12)
	assert.False(t, c.ReadOnly)

	other := testJob()
	other.Repo = "someone/else"
	assert.NotEqual(t, c.Key, plan(other).Key, "each repository has its own caches")

	fork := prTestJob()
	fork.Fork = true
	forkCache := plan(fork)
	assert.Equal(t, c.Key, forkCache.Key, "a fork reads the repository's caches")
	assert.True(t, forkCache.ReadOnly, "but cannot write them")

	p, err := Planner{Configs: &fakeConfigs{data: []byte(strings.Replace(cacheConfig, "cache:\n  - /go/pkg/mod\n  - /root/.cache/go-build\n", "", 1))}}.Plan(t.Context(), testJob())
	require.NoError(t, err)
	assert.Nil(t, p.Assignment.Cache)
}
