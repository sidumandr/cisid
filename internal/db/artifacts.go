package db

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

var (
	// ErrArtifactNotFound is returned when a job has no artifact of the
	// given name.
	ErrArtifactNotFound = errors.New("artifact not found")
	// ErrArtifactTooLarge is returned when an upload is over its limit.
	ErrArtifactTooLarge = errors.New("artifact too large")
	// ErrTooManyArtifacts is returned when a job already has the most
	// artifacts it may.
	ErrTooManyArtifacts = errors.New("too many artifacts")
)

// artifactChunk is how much content one row holds.
const artifactChunk = 1 << 20

const artifactColumns = "id, job_id, name, size, sha256, created_at"

func scanArtifact(row pgx.CollectableRow) (models.Artifact, error) {
	var a models.Artifact
	err := row.Scan(&a.ID, &a.JobID, &a.Name, &a.Size, &a.SHA256, &a.CreatedAt)
	return a, err
}

// ArtifactLimits bound what a job may upload.
type ArtifactLimits struct {
	MaxSize  int64 // bytes per artifact
	MaxCount int   // artifacts per job
}

// PutArtifact stores the content of r as artifact name of job jobID, which
// must be running on agent (ErrJobConflict otherwise), replacing an
// artifact of the same name. Content goes in chunks, each in its own
// statement, so a large upload holds no transaction open; the artifact
// counts only once all of it is stored.
func (s *Store) PutArtifact(ctx context.Context, jobID int64, agent, name string, r io.Reader, limits ArtifactLimits) (models.Artifact, error) {
	if !models.ValidArtifactName(name) {
		return models.Artifact{}, fmt.Errorf("%w: invalid artifact name %q", ErrInvalidJobResult, name)
	}
	var running bool
	err := s.pool.QueryRow(ctx, "SELECT EXISTS (SELECT 1 FROM jobs WHERE id = $1 AND status = 'running' AND agent = $2)",
		jobID, agent).Scan(&running)
	if err != nil {
		return models.Artifact{}, fmt.Errorf("check job %d: %w", jobID, err)
	}
	if !running {
		return models.Artifact{}, fmt.Errorf("%w: job %d is not running on %s", ErrJobConflict, jobID, agent)
	}

	if _, err := s.pool.Exec(ctx, "DELETE FROM artifacts WHERE job_id = $1 AND name = $2", jobID, name); err != nil {
		return models.Artifact{}, fmt.Errorf("replace artifact %s of job %d: %w", name, jobID, err)
	}
	var id int64
	err = s.pool.QueryRow(ctx, `
		INSERT INTO artifacts (job_id, name)
		SELECT $1, $2 WHERE (SELECT count(*) FROM artifacts WHERE job_id = $1) < $3
		RETURNING id`, jobID, name, limits.MaxCount).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Artifact{}, fmt.Errorf("%w: job %d has %d", ErrTooManyArtifacts, jobID, limits.MaxCount)
	}
	if err != nil {
		return models.Artifact{}, fmt.Errorf("store artifact %s of job %d: %w", name, jobID, err)
	}

	a, err := s.writeChunks(ctx, id, r, limits.MaxSize)
	if err != nil {
		// Without the incomplete row, a retry starts afresh.
		dctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		_, _ = s.pool.Exec(dctx, "DELETE FROM artifacts WHERE id = $1", id)
		return models.Artifact{}, err
	}
	return a, nil
}

// cleanupTimeout bounds removing what a failed upload left.
const cleanupTimeout = 10 * time.Second

// PruneArtifacts deletes the incomplete artifacts older than age: uploads
// a server stopped in the middle of.
func (s *Store) PruneArtifacts(ctx context.Context, age time.Duration) (int, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM artifacts WHERE NOT complete AND created_at < now() - make_interval(secs => $1)", age.Seconds())
	if err != nil {
		return 0, fmt.Errorf("prune incomplete artifacts: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

func (s *Store) writeChunks(ctx context.Context, id int64, r io.Reader, maxSize int64) (models.Artifact, error) {
	sum := sha256.New()
	buf := make([]byte, artifactChunk)
	var size int64
	for n := 0; ; n++ {
		k, err := io.ReadFull(r, buf)
		if k > 0 {
			size += int64(k)
			if size > maxSize {
				return models.Artifact{}, fmt.Errorf("%w: over %d bytes", ErrArtifactTooLarge, maxSize)
			}
			sum.Write(buf[:k])
			if _, err := s.pool.Exec(ctx, "INSERT INTO artifact_chunks (artifact_id, n, data) VALUES ($1, $2, $3)",
				id, n, buf[:k]); err != nil {
				return models.Artifact{}, fmt.Errorf("store artifact chunk: %w", err)
			}
		}
		if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
			break
		}
		if err != nil {
			return models.Artifact{}, fmt.Errorf("read artifact: %w", err)
		}
	}
	rows, err := s.pool.Query(ctx, `
		UPDATE artifacts SET size = $2, sha256 = $3, complete = true WHERE id = $1
		RETURNING `+artifactColumns, id, size, hex.EncodeToString(sum.Sum(nil)))
	if err != nil {
		return models.Artifact{}, fmt.Errorf("complete artifact: %w", err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanArtifact)
	if err != nil {
		return models.Artifact{}, fmt.Errorf("complete artifact: %w", err)
	}
	return a, nil
}

// Artifacts lists the complete artifacts of job jobID by name.
func (s *Store) Artifacts(ctx context.Context, jobID int64) ([]models.Artifact, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE job_id = $1 AND complete ORDER BY name", jobID)
	if err != nil {
		return nil, fmt.Errorf("list artifacts of job %d: %w", jobID, err)
	}
	list, err := pgx.CollectRows(rows, scanArtifact)
	if err != nil {
		return nil, fmt.Errorf("list artifacts of job %d: %w", jobID, err)
	}
	return list, nil
}

// Artifact returns the complete artifact name of job jobID, or
// ErrArtifactNotFound.
func (s *Store) Artifact(ctx context.Context, jobID int64, name string) (models.Artifact, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+artifactColumns+" FROM artifacts WHERE job_id = $1 AND name = $2 AND complete",
		jobID, name)
	if err != nil {
		return models.Artifact{}, fmt.Errorf("read artifact %s of job %d: %w", name, jobID, err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, scanArtifact)
	if errors.Is(err, pgx.ErrNoRows) {
		return models.Artifact{}, fmt.Errorf("%w: %s of job %d", ErrArtifactNotFound, name, jobID)
	}
	if err != nil {
		return models.Artifact{}, fmt.Errorf("read artifact %s of job %d: %w", name, jobID, err)
	}
	return a, nil
}

// WriteArtifact copies the content of artifact a to w, chunk by chunk.
func (s *Store) WriteArtifact(ctx context.Context, a models.Artifact, w io.Writer) error {
	rows, err := s.pool.Query(ctx, "SELECT data FROM artifact_chunks WHERE artifact_id = $1 ORDER BY n", a.ID)
	if err != nil {
		return fmt.Errorf("read artifact %d: %w", a.ID, err)
	}
	defer rows.Close()
	for rows.Next() {
		var data []byte
		if err := rows.Scan(&data); err != nil {
			return fmt.Errorf("read artifact %d: %w", a.ID, err)
		}
		if _, err := w.Write(data); err != nil {
			return fmt.Errorf("write artifact %d: %w", a.ID, err)
		}
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("read artifact %d: %w", a.ID, err)
	}
	return nil
}
