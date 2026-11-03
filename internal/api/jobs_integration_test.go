package api

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/db/dbtest"
	"github.com/sidumandr/cisid/internal/events"
	"github.com/sidumandr/cisid/pkg/models"
)

// TestStreamIntegrationLiveLogs follows a running job through PostgreSQL
// notifications alone: the fallback poll is disabled.
func TestStreamIntegrationLiveLogs(t *testing.T) {
	pool := dbtest.New(t)
	_, err := db.Migrate(t.Context(), pool)
	require.NoError(t, err)
	store := db.New(pool)
	discard := slog.New(slog.DiscardHandler)

	broker := events.New(discard)
	ctx, stop := context.WithCancel(t.Context())
	listening := make(chan struct{})
	go func() {
		defer close(listening)
		assert.NoError(t, broker.Listen(ctx, func(ctx context.Context) (*pgx.Conn, error) {
			c, err := pool.Acquire(ctx)
			if err != nil {
				return nil, err
			}
			return c.Hijack(), nil
		}))
	}()
	t.Cleanup(func() { stop(); <-listening })

	id, err := store.CreateJob(t.Context(), models.Job{
		Repo: "sidumandr/cisid", CloneURL: "https://github.com/sidumandr/cisid.git",
		Branch: "main", CommitSHA: "6666666666666666666666666666666666666666", Status: models.JobStatusPending,
	})
	require.NoError(t, err)
	_, err = store.ClaimJob(t.Context(), "agent-1", time.Minute)
	require.NoError(t, err)

	jobs := NewJobsAPI(store, broker, discard)
	jobs.pollInterval = time.Hour
	t.Cleanup(jobs.Close)
	h := NewRouter(Handlers{Webhook: http.NotFoundHandler(), Agents: http.NotFoundHandler(), Jobs: jobs, DB: store}, discard)

	stream := openStream(t, h, fmt.Sprintf("/api/jobs/%d/logs/stream", id), nil)
	assert.Equal(t, models.JobStatusRunning, next(t, stream).job(t).Status)

	for seq := int64(1); seq <= 3; seq++ {
		data := fmt.Sprintf("line %d\n", seq)
		require.NoError(t, store.AppendJobLog(t.Context(), models.JobLog{JobID: id, Seq: seq, Stream: models.StreamStdout, Data: data}))
		got := next(t, stream).log(t)
		assert.Equal(t, seq, got.Seq)
		assert.Equal(t, data, got.Data)
	}

	require.NoError(t, store.FinishJob(t.Context(), "agent-1", models.JobResult{JobID: id, Status: models.JobStatusFailed, ExitCode: 1}))
	assert.Equal(t, models.JobStatusFailed, next(t, stream).job(t).Status)
	expectEnd(t, stream)
}
