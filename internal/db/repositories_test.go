package db

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

func TestStoreRepositories(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()

	first, created, err := s.PutRepository(ctx, models.Repository{Repo: "sidumandr/biopotan", CloneURL: "https://github.com/sidumandr/biopotan.git",
		DefaultBranch: "main", AddedBy: "ilhan"})
	require.NoError(t, err)
	assert.True(t, created)
	assert.WithinDuration(t, time.Now(), first.AddedAt, time.Minute, "the stored repository comes back")
	assert.False(t, first.Polled)
	again, created, err := s.PutRepository(ctx, models.Repository{Repo: "sidumandr/biopotan", CloneURL: "https://github.com/sidumandr/biopotan.git",
		DefaultBranch: "trunk", AddedBy: "someone else"})
	require.NoError(t, err)
	assert.False(t, created, "connecting again only updates it")
	assert.Equal(t, "ilhan", again.AddedBy, "and says who connected it first")
	assert.Equal(t, "trunk", again.DefaultBranch)
	assert.True(t, first.AddedAt.Equal(again.AddedAt))
	_, _, err = s.PutRepository(ctx, models.Repository{Repo: "gitlab:group/app", CloneURL: "https://gitlab.com/group/app.git", DefaultBranch: "main"})
	require.NoError(t, err)
	_, err = s.PutWatch(ctx, models.Watch{Repo: "sidumandr/biopotan", Branch: "trunk", CloneURL: "u"})
	require.NoError(t, err)

	list, err := s.Repositories(ctx)
	require.NoError(t, err)
	require.Len(t, list, 2)
	assert.Equal(t, "gitlab:group/app", list[0].Repo, "listed by key")
	assert.False(t, list[0].Polled)
	got := list[1]
	assert.Equal(t, "trunk", got.DefaultBranch)
	assert.Equal(t, "ilhan", got.AddedBy, "who connected it first stays")
	assert.True(t, got.Polled)
	assert.WithinDuration(t, time.Now(), got.AddedAt, time.Minute)

	require.NoError(t, s.PutSchedule(ctx, models.Schedule{Repo: "sidumandr/biopotan", Name: "nightly", Cron: "0 3 * * *", NextRunAt: time.Now()}))
	found, err := s.DeleteRepository(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.True(t, found)
	watches, err := s.Watches(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.Empty(t, watches, "disconnecting stops the polling")
	schedules, err := s.Schedules(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.Empty(t, schedules, "and the schedules")

	found, err = s.DeleteRepository(ctx, "sidumandr/biopotan")
	require.NoError(t, err)
	assert.False(t, found)

	// A repository only polled, from before repositories were connected,
	// can be disconnected too.
	_, err = s.PutWatch(ctx, models.Watch{Repo: "old/polled", Branch: "main", CloneURL: "u"})
	require.NoError(t, err)
	found, err = s.DeleteRepository(ctx, "old/polled")
	require.NoError(t, err)
	assert.True(t, found)
}
