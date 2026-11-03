// Package events tells interested readers, such as live log streams, that a
// job has changed. Changes are announced by PostgreSQL NOTIFY, so a stream
// on one server sees jobs whose agent is connected to another.
package events

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/internal/db"
)

const reconnectDelay = time.Second

// Broker fans job change signals out to subscribers.
type Broker struct {
	log *slog.Logger

	mu   sync.Mutex
	subs map[int64]map[chan struct{}]struct{}
}

// New returns a Broker. A nil log uses slog.Default().
func New(log *slog.Logger) *Broker {
	if log == nil {
		log = slog.Default()
	}
	return &Broker{log: log, subs: map[int64]map[chan struct{}]struct{}{}}
}

// Subscribe returns a channel that receives a value whenever job jobID may
// have changed. Signals are coalesced: a reader re-reads the job's state
// after each one. Call the returned func to unsubscribe.
func (b *Broker) Subscribe(jobID int64) (<-chan struct{}, func()) {
	ch := make(chan struct{}, 1)
	b.mu.Lock()
	if b.subs[jobID] == nil {
		b.subs[jobID] = map[chan struct{}]struct{}{}
	}
	b.subs[jobID][ch] = struct{}{}
	b.mu.Unlock()

	return ch, func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.subs[jobID], ch)
		if len(b.subs[jobID]) == 0 {
			delete(b.subs, jobID)
		}
	}
}

// Publish signals the subscribers of jobID. It never blocks.
func (b *Broker) Publish(jobID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs[jobID] {
		signal(ch)
	}
}

// publishAll signals every subscriber, e.g. after notifications may have
// been missed while reconnecting.
func (b *Broker) publishAll() {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, chans := range b.subs {
		for ch := range chans {
			signal(ch)
		}
	}
}

func signal(ch chan struct{}) {
	select {
	case ch <- struct{}{}:
	default:
	}
}

// Listen receives notifications on db.JobEventsChannel through connections
// from connect and publishes them, until ctx is canceled. A lost connection
// is replaced after a short delay; subscribers are then signaled in case
// they missed something.
func (b *Broker) Listen(ctx context.Context, connect func(context.Context) (*pgx.Conn, error)) error {
	for {
		err := b.listen(ctx, connect)
		if ctx.Err() != nil {
			return nil
		}
		b.log.WarnContext(ctx, "job event listener stopped, reconnecting", "error", err, "retry_in", reconnectDelay)

		t := time.NewTimer(reconnectDelay)
		select {
		case <-ctx.Done():
			t.Stop()
			return nil
		case <-t.C:
		}
	}
}

func (b *Broker) listen(ctx context.Context, connect func(context.Context) (*pgx.Conn, error)) error {
	conn, err := connect(ctx)
	if err != nil {
		return fmt.Errorf("connect: %w", err)
	}
	defer conn.Close(context.WithoutCancel(ctx))

	if _, err := conn.Exec(ctx, "LISTEN "+db.JobEventsChannel); err != nil {
		return fmt.Errorf("listen on %s: %w", db.JobEventsChannel, err)
	}
	b.publishAll()

	for {
		n, err := conn.WaitForNotification(ctx)
		if err != nil {
			return fmt.Errorf("wait for notification: %w", err)
		}
		id, err := strconv.ParseInt(n.Payload, 10, 64)
		if err != nil {
			b.log.WarnContext(ctx, "ignoring malformed job event", "payload", n.Payload)
			continue
		}
		b.Publish(id)
	}
}
