package runner

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/moby/moby/api/types/container"
)

// DefaultPids caps the processes of a container, which stops fork bombs
// from taking the host down.
const DefaultPids = 4096

// Limits bounds the resources of every step and service container.
// Zero means no limit.
type Limits struct {
	MemoryBytes int64
	NanoCPUs    int64
	Pids        int64
}

// WithLimits applies limits to every container the runner starts.
func WithLimits(l Limits) Option {
	return func(r *DockerRunner) { r.limits = l }
}

// hostConfig adds the runner's limits and hardening to hc.
func (l Limits) apply(hc *container.HostConfig) {
	hc.Resources.Memory = l.MemoryBytes
	if l.MemoryBytes > 0 {
		hc.Resources.MemorySwap = l.MemoryBytes // no swap beyond the limit
	}
	hc.Resources.NanoCPUs = l.NanoCPUs
	if l.Pids > 0 {
		pids := l.Pids
		hc.Resources.PidsLimit = &pids
	}
	// Setuid binaries cannot gain privileges the container did not start with.
	hc.SecurityOpt = append(hc.SecurityOpt, "no-new-privileges:true")
}

// ParseMemory parses a size such as "512m" or "2g" (binary units: k, m, g)
// into bytes. An empty string is 0.
func ParseMemory(s string) (int64, error) {
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return 0, nil
	}
	mult := int64(1)
	switch {
	case strings.HasSuffix(s, "k"):
		mult, s = 1<<10, strings.TrimSuffix(s, "k")
	case strings.HasSuffix(s, "m"):
		mult, s = 1<<20, strings.TrimSuffix(s, "m")
	case strings.HasSuffix(s, "g"):
		mult, s = 1<<30, strings.TrimSuffix(s, "g")
	}
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil || n <= 0 {
		return 0, fmt.Errorf("invalid memory size %q: want e.g. 512m or 2g", s)
	}
	return n * mult, nil
}

// ParseCPUs parses a CPU count such as "1.5" into NanoCPUs. An empty
// string is 0.
func ParseCPUs(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, nil
	}
	f, err := strconv.ParseFloat(s, 64)
	if err != nil || f <= 0 {
		return 0, fmt.Errorf("invalid CPU count %q: want e.g. 2 or 0.5", s)
	}
	return int64(f * 1e9), nil
}
