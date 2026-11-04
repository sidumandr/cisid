package db

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/pkg/models"
)

var testLimits = ArtifactLimits{MaxSize: 3 << 20, MaxCount: 3}

func TestStoreArtifacts(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)

	// Over a chunk, so it is stored in pieces.
	big := bytes.Repeat([]byte("cisid "), (artifactChunk/6)+1000)
	a, err := s.PutArtifact(ctx, job.ID, "worker-1", "dist/app.bin", bytes.NewReader(big), testLimits)
	require.NoError(t, err)
	assert.Equal(t, int64(len(big)), a.Size)
	want := sha256.Sum256(big)
	assert.Equal(t, hex.EncodeToString(want[:]), a.SHA256)
	var chunks int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM artifact_chunks WHERE artifact_id = $1", a.ID).Scan(&chunks))
	assert.Equal(t, 2, chunks)

	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "coverage.out", strings.NewReader("mode: set\n"), testLimits)
	require.NoError(t, err)
	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "empty.txt", strings.NewReader(""), testLimits)
	require.NoError(t, err)

	list, err := s.Artifacts(ctx, job.ID)
	require.NoError(t, err)
	require.Len(t, list, 3)
	assert.Equal(t, []string{"coverage.out", "dist/app.bin", "empty.txt"}, []string{list[0].Name, list[1].Name, list[2].Name})
	assert.Zero(t, list[2].Size)

	got, err := s.Artifact(ctx, job.ID, "dist/app.bin")
	require.NoError(t, err)
	var out bytes.Buffer
	require.NoError(t, s.WriteArtifact(ctx, got, &out))
	assert.Equal(t, big, out.Bytes(), "the content comes back whole")

	_, err = s.Artifact(ctx, job.ID, "missing")
	require.ErrorIs(t, err, ErrArtifactNotFound)

	// Uploading a name again replaces it, and does not count twice.
	a, err = s.PutArtifact(ctx, job.ID, "worker-1", "coverage.out", strings.NewReader("mode: count\n"), testLimits)
	require.NoError(t, err)
	assert.EqualValues(t, len("mode: count\n"), a.Size)
	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "fourth.txt", strings.NewReader("x"), testLimits)
	require.ErrorIs(t, err, ErrTooManyArtifacts)

	_, err = s.PutArtifact(ctx, job.ID, "worker-2", "x.txt", strings.NewReader("x"), testLimits)
	require.ErrorIs(t, err, ErrJobConflict, "only the agent running the job uploads")
	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "../escape", strings.NewReader("x"), testLimits)
	require.Error(t, err)
}

func TestStoreArtifactTooLarge(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)

	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "huge.bin", bytes.NewReader(make([]byte, 5<<20)), testLimits)
	require.ErrorIs(t, err, ErrArtifactTooLarge)
	var n int
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM artifacts").Scan(&n))
	assert.Zero(t, n, "nothing is left of a refused upload")
	require.NoError(t, pool.QueryRow(ctx, "SELECT count(*) FROM artifact_chunks").Scan(&n))
	assert.Zero(t, n)
}

// failingReader breaks off after some bytes, like a dropped connection.
type failingReader struct{ left int }

func (r *failingReader) Read(p []byte) (int, error) {
	if r.left == 0 {
		return 0, errors.New("connection reset")
	}
	n := min(len(p), r.left)
	r.left -= n
	return n, nil
}

func TestStoreArtifactBrokenUpload(t *testing.T) {
	s, _ := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)

	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "app.bin", &failingReader{left: artifactChunk + 10}, testLimits)
	require.Error(t, err)
	list, err := s.Artifacts(ctx, job.ID)
	require.NoError(t, err)
	assert.Empty(t, list)
}

func TestStorePruneAndExpireArtifacts(t *testing.T) {
	s, pool := newStore(t)
	ctx := t.Context()
	createJobs(t, s, 1)
	job, err := s.ClaimJob(ctx, "worker-1", time.Minute)
	require.NoError(t, err)
	_, err = s.PutArtifact(ctx, job.ID, "worker-1", "kept.txt", strings.NewReader("x"), testLimits)
	require.NoError(t, err)
	_, err = pool.Exec(ctx, "INSERT INTO artifacts (job_id, name, created_at) VALUES ($1, 'half.bin', now() - interval '2 days')", job.ID)
	require.NoError(t, err)

	n, err := s.PruneArtifacts(ctx, 24*time.Hour)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "only the old incomplete upload")

	require.NoError(t, s.FinishJob(ctx, "worker-1", models.JobResult{JobID: job.ID, Status: models.JobStatusSuccess}))
	_, err = pool.Exec(ctx, "UPDATE jobs SET finished_at = now() - interval '100 days' WHERE id = $1", job.ID)
	require.NoError(t, err)
	_, err = s.ExpireLogs(ctx, 90*24*time.Hour, 10)
	require.NoError(t, err)
	list, err := s.Artifacts(ctx, job.ID)
	require.NoError(t, err)
	assert.Empty(t, list, "artifacts go with the logs")
}
