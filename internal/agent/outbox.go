package agent

import (
	"context"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/sidumandr/cisid/pkg/models"
)

// outbox holds messages for the server until it acknowledges them. It
// outlives individual connections: messages produced while disconnected,
// and messages sent on a connection that broke before they were
// acknowledged, are sent after reconnecting.
type outbox struct {
	// ch holds messages not yet sent, in order.
	ch chan models.Message

	mu sync.Mutex
	// inflight holds sent, unacknowledged messages, ordered by ID.
	inflight []models.Message
	lastID   int64

	// pending counts queued plus unacknowledged messages.
	pending atomic.Int64
	dropped atomic.Int64
	size    int64
}

func newOutbox(size int) *outbox {
	return &outbox{ch: make(chan models.Message, size), size: int64(size)}
}

// tryPut queues msg without blocking and reports whether it was queued.
// Used for log chunks: a slow or absent server must not stall a job.
func (o *outbox) tryPut(msg models.Message) bool {
	if o.pending.Add(1) > o.size {
		o.pending.Add(-1)
		o.dropped.Add(1)
		return false
	}
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

// take numbers a message just read from ch and keeps it until it is
// acknowledged. Only the writer calls it, so IDs follow the send order.
func (o *outbox) take(msg models.Message) models.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.lastID++
	msg.ID = o.lastID
	o.inflight = append(o.inflight, msg)
	return msg
}

// unacked returns the sent but unacknowledged messages, oldest first.
func (o *outbox) unacked() []models.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	return slices.Clone(o.inflight)
}

// ack forgets the messages with IDs up to and including id and returns them.
func (o *outbox) ack(id int64) []models.Message {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := 0
	for n < len(o.inflight) && o.inflight[n].ID <= id {
		n++
	}
	acked := slices.Clone(o.inflight[:n])
	o.inflight = slices.Delete(o.inflight, 0, n)
	o.pending.Add(-int64(n))
	return acked
}

// flush waits until every queued message has been acknowledged or ctx is done.
func (o *outbox) flush(ctx context.Context) error {
	tick := time.NewTicker(20 * time.Millisecond)
	defer tick.Stop()

	for o.pending.Load() > 0 {
		select {
		case <-ctx.Done():
			return fmt.Errorf("flush outbox, %d messages unacknowledged: %w", o.pending.Load(), ctx.Err())
		case <-tick.C:
		}
	}
	return nil
}
