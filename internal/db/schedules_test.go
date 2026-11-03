package db

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

// hourly moves a schedule an hour on; "bad" stands for a cron that no
// longer parses.
func hourly(cron string, after time.Time) (time.Time, error) {
	if cron == "bad" {
		return time.Time{}, errors.New("bad cron")
	}
	return after.Add(time.Hour), nil
}

func TestStoreSchedules(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	const repo = "sidumandr/cisid"
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)

	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: repo, Name: "nightly", Cron: "0 3 * * *", NextRunAt: now.Add(-time.Minute)}))
	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: repo, Name: "later", Cron: "0 4 * * *", Branch: "dev", NextRunAt: now.Add(time.Hour)}))
	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: repo, Name: "broken", Cron: "bad", NextRunAt: now.Add(-time.Hour)}))
	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: "other/repo", Name: "nightly", Cron: "0 3 * * *", NextRunAt: now.Add(time.Hour)}))
	// Putting again replaces cron, branch and next run.
	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: repo, Name: "later", Cron: "0 5 * * *", Branch: "main", NextRunAt: now.Add(2 * time.Hour)}))

	list, err := s.Schedules(ctx, repo)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []string{"broken", "later", "nightly"}, []string{list[0].Name, list[1].Name, list[2].Name})
	assert.Equal(t, "0 5 * * *", list[1].Cron)
	assert.Equal(t, "main", list[1].Branch)
	assert.Nil(t, list[2].LastRunAt)

	due, err := s.ClaimDueSchedules(ctx, now, hourly, 10)
	require.NoError(t, err)
	require.Len(t, due, 2, "only what is due")
	assert.Equal(t, "broken", due[0].Name, "the most overdue first")
	assert.Equal(t, "nightly", due[1].Name)
	assert.True(t, due[1].NextRunAt.Equal(now.Add(time.Hour)))

	again, err := s.ClaimDueSchedules(ctx, now, hourly, 10)
	require.NoError(t, err)
	assert.Empty(t, again, "a claimed run is not claimed twice")

	list, err = s.Schedules(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, 9999, list[0].NextRunAt.Year(), "a cron that no longer parses is parked")
	require.NotNil(t, list[2].LastRunAt)
	assert.True(t, list[2].LastRunAt.Equal(now))

	jobID := createJobs(t, s, 1)[0]
	require.NoError(t, s.MarkScheduleRun(ctx, list[2].ID, jobID))
	list, err = s.Schedules(ctx, repo)
	require.NoError(t, err)
	assert.Equal(t, &jobID, list[2].LastJobID)

	found, err := s.DeleteSchedule(ctx, repo, "nightly")
	require.NoError(t, err)
	assert.True(t, found)
	found, err = s.DeleteSchedule(ctx, repo, "nightly")
	require.NoError(t, err)
	assert.False(t, found)
}

// TestStoreClaimDueSchedulesOnce claims from several "servers" at once:
// every run is claimed exactly once.
func TestStoreClaimDueSchedulesOnce(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	now := time.Date(2026, 11, 3, 12, 0, 0, 0, time.UTC)
	const n = 20
	for i := range n {
		require.NoError(t, s.PutSchedule(ctx, models.Schedule{
			Repo: "sidumandr/cisid", Name: "s" + string(rune('a'+i)), Cron: "0 3 * * *", NextRunAt: now.Add(-time.Minute),
		}))
	}

	var (
		mu      sync.Mutex
		claimed = map[string]int{}
		wg      sync.WaitGroup
	)
	for range 4 {
		wg.Go(func() {
			for {
				due, err := s.ClaimDueSchedules(ctx, now, hourly, 3)
				if !assert.NoError(t, err) || len(due) == 0 {
					return
				}
				mu.Lock()
				for _, d := range due {
					claimed[d.Name]++
				}
				mu.Unlock()
			}
		})
	}
	wg.Wait()

	assert.Len(t, claimed, n)
	for name, times := range claimed {
		assert.Equal(t, 1, times, name)
	}
}
