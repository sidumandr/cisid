package runner

import (
	"testing"

	"github.com/moby/moby/api/types/mount"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestVolumeMountLabels(t *testing.T) {
	opts := createOptions(Step{Image: "alpine:3.22", Mounts: []Mount{
		{Type: MountVolume, Source: "cisid-cache-k", Target: "/cache", Labels: map[string]string{"cisid.cache": "k"}},
		{Source: "/src", Target: "/src", Labels: map[string]string{"ignored": "x"}},
	}})

	mounts := opts.HostConfig.Mounts
	require.Len(t, mounts, 2)
	assert.Equal(t, &mount.VolumeOptions{Labels: map[string]string{"cisid.cache": "k"}}, mounts[0].VolumeOptions,
		"labels go on the volume Docker creates")
	assert.Nil(t, mounts[1].VolumeOptions, "a bind mount has no volume")
}
