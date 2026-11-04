package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/sidumandr/cisid/pkg/models"
)

// ErrForgeAccountNotFound is returned when a user has not connected a forge.
var ErrForgeAccountNotFound = errors.New("forge account not found")

// SealedForgeAccount is a user's access to a forge as it is stored: its
// tokens sealed.
type SealedForgeAccount struct {
	UserID    int64
	Forge     string
	Login     string
	Kind      string // oauth or token
	Token     []byte
	Refresh   []byte // nil without a refresh token
	ExpiresAt *time.Time
	CreatedAt time.Time
}

// PutForgeAccount connects a.Forge for a.UserID, or replaces the access
// they connected before.
func (s *Store) PutForgeAccount(ctx context.Context, a SealedForgeAccount) error {
	const q = `
		INSERT INTO forge_accounts (user_id, forge, login, kind, token, refresh, expires_at) VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (user_id, forge) DO UPDATE SET login = EXCLUDED.login, kind = EXCLUDED.kind, token = EXCLUDED.token,
			refresh = EXCLUDED.refresh, expires_at = EXCLUDED.expires_at`
	if _, err := s.pool.Exec(ctx, q, a.UserID, a.Forge, a.Login, a.Kind, a.Token, a.Refresh, a.ExpiresAt); err != nil {
		return fmt.Errorf("store %s account of user %d: %w", a.Forge, a.UserID, err)
	}
	return nil
}

// SealedForgeAccount returns the access userID connected to forge.
func (s *Store) SealedForgeAccount(ctx context.Context, userID int64, forge string) (SealedForgeAccount, error) {
	const q = `SELECT user_id, forge, login, kind, token, refresh, expires_at, created_at
		FROM forge_accounts WHERE user_id = $1 AND forge = $2`
	rows, err := s.pool.Query(ctx, q, userID, forge)
	if err != nil {
		return SealedForgeAccount{}, fmt.Errorf("read %s account of user %d: %w", forge, userID, err)
	}
	a, err := pgx.CollectExactlyOneRow(rows, pgx.RowToStructByPos[SealedForgeAccount])
	if errors.Is(err, pgx.ErrNoRows) {
		return SealedForgeAccount{}, fmt.Errorf("%w: user %d, %s", ErrForgeAccountNotFound, userID, forge)
	}
	if err != nil {
		return SealedForgeAccount{}, fmt.Errorf("read %s account of user %d: %w", forge, userID, err)
	}
	return a, nil
}

// ForgeAccounts lists the forges userID connected, without their tokens.
func (s *Store) ForgeAccounts(ctx context.Context, userID int64) ([]models.ForgeAccount, error) {
	rows, err := s.pool.Query(ctx, "SELECT forge, login, kind, created_at FROM forge_accounts WHERE user_id = $1 ORDER BY forge", userID)
	if err != nil {
		return nil, fmt.Errorf("list forge accounts of user %d: %w", userID, err)
	}
	accounts, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.ForgeAccount])
	if err != nil {
		return nil, fmt.Errorf("list forge accounts of user %d: %w", userID, err)
	}
	return accounts, nil
}

// DeleteForgeAccount disconnects forge for userID and reports whether it
// was connected. Repositories connected with it go back to the server's
// token.
func (s *Store) DeleteForgeAccount(ctx context.Context, userID int64, forge string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM forge_accounts WHERE user_id = $1 AND forge = $2", userID, forge)
	if err != nil {
		return false, fmt.Errorf("delete %s account of user %d: %w", forge, userID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// RepositoryUser returns whose access the connected repository repo is
// reached with; false for the server's own.
func (s *Store) RepositoryUser(ctx context.Context, repo string) (int64, bool, error) {
	var id *int64
	err := s.pool.QueryRow(ctx, "SELECT via_user_id FROM repositories WHERE repo = $1", repo).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("read whose access %s uses: %w", repo, err)
	}
	if id == nil {
		return 0, false, nil
	}
	return *id, true, nil
}
