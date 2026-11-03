package agent

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestBackoffGrowsAndCaps(t *testing.T) {
	b := newBackoff(100*time.Millisecond, time.Second)
	b.jitter = func(d time.Duration) time.Duration { return d }

	var got []time.Duration
	for range 6 {
		got = append(got, b.Next())
	}
	assert.Equal(t, []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		time.Second,
		time.Second,
	}, got)

	b.Reset()
	assert.Equal(t, 100*time.Millisecond, b.Next())
}

func TestBackoffDoesNotOverflow(t *testing.T) {
	b := newBackoff(time.Second, time.Hour)
	b.jitter = func(d time.Duration) time.Duration { return d }

	for range 100 {
		d := b.Next()
		assert.Positive(t, d)
		assert.LessOrEqual(t, d, time.Hour)
	}
}

func TestBackoffJitterStaysInRange(t *testing.T) {
	b := newBackoff(time.Second, time.Second)

	for range 1000 {
		d := b.Next()
		assert.GreaterOrEqual(t, d, 500*time.Millisecond)
		assert.LessOrEqual(t, d, time.Second)
	}
}
