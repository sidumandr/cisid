package agent

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/sidumandr/cisid/pkg/models"
)

// outbox queues messages for the server. It outlives individual connections,
// so messages produced while disconnected are sent after reconnecting.
// Only the connection's writer goroutine takes messages out of it.
type outbox struct {
	ch chan models.Message
	// pending counts messages queued or being written.
	pending atomic.Int64
	dropped atomic.Int64
}

func newOutbox(size int) *outbox {
	return &outbox{ch: make(chan models.Message, size)}
}

// tryPut queues msg without blocking and reports whether it was queued.
// Used for log chunks: a slow or absent server must not stall a job.
func (o *outbox) tryPut(msg models.Message) bool {
	o.pending.Add(1)
	select {
	case o.ch <- msg:
		return true
	default:
		o.pending.Add(-1)
		o.dropped.Add(1)
		return false
	}
}

// put queues msg, waiting for space until ctx is done.
func (o *outbox) put(ctx context.Context, msg models.Message) error {
	o.pending.Add(1)
	select {
	case o.ch <- msg:
		return nil
	case <-ctx.Done():
		o.pending.Add(-1)
		return fmt.Errorf("queue %s message: %w", msg.Type, ctx.Err())
	}
}

// written marks a message taken from ch as handled, whether or not the
// write succeeded.
func (o *outbox) written() {
	o.pending.Add(-1)
}

// flush waits until every queued message has been handled or ctx is done.
func (o *outbox) flush(ctx context.Context) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	for o.pending.Load() > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("flush outbox, %d messages undelivered: %w", o.pending.Load(), ctx.Err())
		case <-tick.C:
		}
	}
	return nil
}
