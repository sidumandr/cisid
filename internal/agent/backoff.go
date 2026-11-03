package agent

import (
	"math/rand/v2"
	"time"
)

// backoff produces exponentially growing reconnect delays between min and
// max, with jitter so that many agents do not reconnect in lockstep.
type backoff struct {
	min, max time.Duration
	attempt  int
	jitter   func(time.Duration) time.Duration
}

func newBackoff(min, max time.Duration) *backoff {
	return &backoff{min: min, max: max, jitter: halfJitter}
}

// Next returns the delay before the next attempt.
func (b *backoff) Next() time.Duration {
	d := b.min
	for i := 0; i < b.attempt && d < b.max; i++ {
		d *= 2
	}
	if d > b.max || d <= 0 {
		d = b.max
	}
	b.attempt++
	return b.jitter(d)
}

// Reset starts over from the minimum delay.
func (b *backoff) Reset() {
	b.attempt = 0
}

// halfJitter returns a random duration in [d/2, d].
func halfJitter(d time.Duration) time.Duration {
	return d/2 + rand.N(d/2+1)
}
