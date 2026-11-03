package agent

import (
	"context"
	"crypto/rand"
	"os"
	"strings"
	"testing"

	"github.com/moby/moby/client"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/runner"
	"github.com/sidumandr/cisid/pkg/models"
)

func TestAgentMountsCaches(t *testing.T) {
	fs := newFakeServer(t)
	r := &mockRunner{}
	var mounts []runner.Mount
	r.On("Run", mock.Anything, mock.MatchedBy(func(s runner.Step) bool {
		mounts = s.Mounts
		return true
	}), mock.Anything, mock.Anything).Return(0, nil).Once()
	r.On("RemoveVolume", mock.Anything, mock.Anything).Return(nil).Once()

	startAgent(t, fs, r)
	sc := fs.accept(t)
	sc.send(t, models.MsgJobAssigned, models.JobAssignment{
		JobID: 12,
		Steps: []models.StepSpec{{Name: "test", Image: "golang:1.27", Commands: []string{"go test ./..."}}},
		Cache: &models.CacheSpec{Key: "abc123", Paths: []string{"/go/pkg/mod"}, ReadOnly: true},
	})
	_, res := sc.collectJob(t, 12)

	require.Equal(t, models.JobStatusSuccess, res.Status)
	require.Len(t, mounts, 2, "the workspace and the cache")
	c := mounts[1]
	assert.Equal(t, runner.MountVolume, c.Type)
	assert.True(t, strings.HasPrefix(c.Source, "cisid-cache-abc123-"), c.Source)
	assert.Equal(t, "/go/pkg/mod", c.Target)
	assert.True(t, c.ReadOnly)
	assert.Equal(t, map[string]string{"cisid.cache": "abc123", "cisid.cache.path": "/go/pkg/mod"}, c.Labels)
	r.AssertExpectations(t)
}

func TestAgentRejectsBadCaches(t *testing.T) {
	steps := []models.StepSpec{{Name: "x", Image: "alpine:3.22", Commands: []string{"true"}}}
	for name, c := range map[string]*models.CacheSpec{
		"bad key":       {Key: "../x", Paths: []string{"/cache"}},
		"relative path": {Key: "abc", Paths: []string{"cache"}},
		"workspace":     {Key: "abc", Paths: []string{"/workspace"}},
	} {
		t.Run(name, func(t *testing.T) {
			assert.ErrorIs(t, validateJob(models.JobAssignment{JobID: 1, Steps: steps, Cache: c}), ErrInvalidJob)
		})
	}
}

// TestDockerIntegrationCache runs two jobs against Docker: the second sees
// what the first left in the cache, and a read-only job cannot change it.
// Skipped unless CISID_DOCKER_TESTS=1.
func TestDockerIntegrationCache(t *testing.T) {
	if os.Getenv("CISID_DOCKER_TESTS") != "1" {
		t.Skip("CISID_DOCKER_TESTS not set; skipping Docker integration test")
	}
	cli, err := client.New(client.FromEnv)
	require.NoError(t, err)
	t.Cleanup(func() { _ = cli.Close() })

	key := "itest" + strings.ToLower(rand.Text()[:8])
	docker := runner.NewDockerRunner(cli)
	t.Cleanup(func() {
		// Caches are kept on purpose, so the test removes its own.
		for _, m := range cacheMounts(&models.CacheSpec{Key: key, Paths: []string{"/cache"}}) {
			assert.NoError(t, docker.RemoveVolume(context.Background(), m.Source))
		}
	})
	fs := newFakeServer(t)
	startAgent(t, fs, docker)
	sc := fs.accept(t)
	sc.wait = dockerTimeout

	run := func(id int64, readOnly bool, commands ...string) (string, models.JobResult) {
		sc.send(t, models.MsgJobAssigned, models.JobAssignment{
			JobID: id,
			Steps: []models.StepSpec{{Name: "step", Image: "alpine:3.22", Commands: commands}},
			Cache: &models.CacheSpec{Key: key, Paths: []string{"/cache"}, ReadOnly: readOnly},
		})
		logs, res := sc.collectJob(t, id)
		var out strings.Builder
		for _, l := range logs {
			if l.Stream == models.StreamStdout {
				out.WriteString(l.Data)
			}
		}
		return out.String(), res
	}

	_, res := run(200, false, "echo warm > /cache/state")
	require.Equal(t, models.JobStatusSuccess, res.Status, res.Error)

	out, res := run(201, false, "cat /cache/state")
	require.Equal(t, models.JobStatusSuccess, res.Status, res.Error)
	assert.Equal(t, "warm\n", out, "the next job finds the cache")

	_, res = run(202, true, "echo poisoned > /cache/state")
	assert.Equal(t, models.JobStatusFailed, res.Status, "a read-only job cannot write the cache")
	out, _ = run(203, true, "cat /cache/state")
	assert.Equal(t, "warm\n", out, "but can read it")
}
