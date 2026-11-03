package events

import (
	"context"
	"log/slog"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/db/dbtest"
)

const testTimeout = 5 * time.Second

func received(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	case <-time.After(200 * time.Millisecond):
		return false
	}
}

func TestBrokerPublish(t *testing.T) {
	b := New(slog.New(slog.DiscardHandler))
	a1, unsubA1 := b.Subscribe(1)
	a2, unsubA2 := b.Subscribe(1)
	other, unsubOther := b.Subscribe(2)
	defer unsubA2()
	defer unsubOther()

	b.Publish(1)
	b.Publish(1) // coalesced, never blocks

	assert.True(t, received(a1))
	assert.True(t, received(a2))
	assert.False(t, received(a1), "signals are coalesced")
	assert.False(t, received(other))

	unsubA1()
	b.Publish(1)
	assert.False(t, received(a1), "unsubscribed")
	assert.True(t, received(a2))
}

func TestBrokerUnsubscribeCleansUp(t *testing.T) {
	b := New(nil)
	_, unsub := b.Subscribe(7)
	unsub()
	assert.Empty(t, b.subs)
}

func notify(t *testing.T, pool *pgxpool.Pool, payload string) {
	t.Helper()
	_, err := pool.Exec(t.Context(), "SELECT pg_notify($1, $2)", db.JobEventsChannel, payload)
	require.NoError(t, err)
}

func TestBrokerListen(t *testing.T) {
	pool := dbtest.New(t)
	b := New(slog.New(slog.DiscardHandler))
	ch, unsub := b.Subscribe(42)
	defer unsub()

	var (
		listener  = make(chan *pgx.Conn, 10)
		ctx, stop = context.WithCancel(t.Context())
		done      = make(chan struct{})
	)
	go func() {
		defer close(done)
		assert.NoError(t, b.Listen(ctx, func(ctx context.Context) (*pgx.Conn, error) {
			c, err := pool.Acquire(ctx)
			if err != nil {
				return nil, err
			}
			conn := c.Hijack()
			listener <- conn
			return conn, nil
		}))
	}()
	t.Cleanup(func() {
		stop()
		<-done
	})

	first := <-listener
	require.True(t, received(ch), "subscribers are signaled once listening starts")

	notify(t, pool, "42")
	assert.True(t, received(ch))
	notify(t, pool, "43")
	notify(t, pool, "not-a-number")
	assert.False(t, received(ch), "other jobs and bad payloads are ignored")

	// The database drops the listening connection; the broker reconnects
	// and signals subscribers, which may have missed events meanwhile.
	_, err := pool.Exec(t.Context(), "SELECT pg_terminate_backend($1)", first.PgConn().PID())
	require.NoError(t, err)
	select {
	case <-listener:
	case <-time.After(testTimeout):
		t.Fatal("broker did not reconnect")
	}
	require.Eventually(t, func() bool { return received(ch) }, testTimeout, 10*time.Millisecond)

	notify(t, pool, "42")
	assert.True(t, received(ch), "events flow again after reconnecting")
}
