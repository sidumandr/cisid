package db

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/sidumandr/cisid/pkg/models"
)

var (
	// ErrUserNotFound is returned for a user, session or token that does
	// not exist, has expired or belongs to a disabled user.
	ErrUserNotFound = errors.New("user not found")
	// ErrUserExists is returned when a username or forge login is taken.
	ErrUserExists = errors.New("user already exists")
)

// uniqueViolation reports whether err is PostgreSQL's unique_violation.
func uniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

const userColumns = "u.id, u.username, u.role, u.disabled, u.password_hash <> '', u.created_at"

func scanUser(row pgx.Row) (models.User, error) {
	var u models.User
	err := row.Scan(&u.ID, &u.Username, &u.Role, &u.Disabled, &u.HasPassword, &u.CreatedAt)
	return u, err
}

// CreateUser stores u with passwordHash (empty for none) and its
// identities, and returns it.
func (s *Store) CreateUser(ctx context.Context, u models.User, passwordHash string) (models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return models.User{}, fmt.Errorf("create user %s: %w", u.Username, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	created, err := scanUser(tx.QueryRow(ctx, `
		INSERT INTO users AS u (username, role, password_hash, disabled) VALUES ($1, $2, $3, $4)
		RETURNING `+userColumns, u.Username, u.Role, passwordHash, u.Disabled))
	if uniqueViolation(err) {
		return models.User{}, fmt.Errorf("%w: %s", ErrUserExists, u.Username)
	}
	if err != nil {
		return models.User{}, fmt.Errorf("create user %s: %w", u.Username, err)
	}
	if err := setIdentities(ctx, tx, created.ID, u.Identities); err != nil {
		return models.User{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return models.User{}, fmt.Errorf("create user %s: %w", u.Username, err)
	}
	created.Identities = u.Identities
	return created, nil
}

// setIdentities replaces a user's forge logins. A login that stays keeps
// the subject it was bound to.
func setIdentities(ctx context.Context, tx pgx.Tx, userID int64, identities map[string]string) error {
	if identities == nil {
		return nil
	}
	if _, err := tx.Exec(ctx, "DELETE FROM user_identities WHERE user_id = $1 AND NOT (forge || ':' || login = ANY($2))",
		userID, identityKeys(identities)); err != nil {
		return fmt.Errorf("set identities of user %d: %w", userID, err)
	}
	for forge, login := range identities {
		_, err := tx.Exec(ctx, `
			INSERT INTO user_identities (user_id, forge, login) VALUES ($1, $2, $3)
			ON CONFLICT (user_id, forge) DO UPDATE SET login = EXCLUDED.login,
				subject = CASE WHEN user_identities.login = EXCLUDED.login THEN user_identities.subject ELSE '' END`,
			userID, forge, login)
		if uniqueViolation(err) {
			return fmt.Errorf("%w: %s login %s belongs to another user", ErrUserExists, forge, login)
		}
		if err != nil {
			return fmt.Errorf("set %s identity of user %d: %w", forge, userID, err)
		}
	}
	return nil
}

func identityKeys(identities map[string]string) []string {
	keys := make([]string, 0, len(identities))
	for forge, login := range identities {
		keys = append(keys, forge+":"+login)
	}
	return keys
}

// identities loads the forge logins of users.
func (s *Store) identities(ctx context.Context, users []models.User) error {
	if len(users) == 0 {
		return nil
	}
	byID := make(map[int64]*models.User, len(users))
	ids := make([]int64, len(users))
	for i := range users {
		byID[users[i].ID] = &users[i]
		ids[i] = users[i].ID
	}
	rows, err := s.pool.Query(ctx, "SELECT user_id, forge, login FROM user_identities WHERE user_id = ANY($1)", ids)
	if err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var (
			id           int64
			forge, login string
		)
		if err := rows.Scan(&id, &forge, &login); err != nil {
			return fmt.Errorf("list identities: %w", err)
		}
		u := byID[id]
		if u.Identities == nil {
			u.Identities = map[string]string{}
		}
		u.Identities[forge] = login
	}
	if err := rows.Err(); err != nil {
		return fmt.Errorf("list identities: %w", err)
	}
	return nil
}

// Users lists every user by name.
func (s *Store) Users(ctx context.Context) ([]models.User, error) {
	rows, err := s.pool.Query(ctx, "SELECT "+userColumns+" FROM users u ORDER BY u.username")
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	users, err := pgx.CollectRows(rows, func(r pgx.CollectableRow) (models.User, error) { return scanUser(r) })
	if err != nil {
		return nil, fmt.Errorf("list users: %w", err)
	}
	if err := s.identities(ctx, users); err != nil {
		return nil, err
	}
	return users, nil
}

// CountUsers returns how many users there are.
func (s *Store) CountUsers(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, "SELECT count(*) FROM users").Scan(&n); err != nil {
		return 0, fmt.Errorf("count users: %w", err)
	}
	return n, nil
}

// User returns the user called username and its password hash.
func (s *Store) User(ctx context.Context, username string) (models.User, string, error) {
	var hash string
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users u WHERE u.username = $1", username))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, "", fmt.Errorf("%w: %s", ErrUserNotFound, username)
	}
	if err != nil {
		return models.User{}, "", fmt.Errorf("get user %s: %w", username, err)
	}
	if err := s.pool.QueryRow(ctx, "SELECT password_hash FROM users WHERE id = $1", u.ID).Scan(&hash); err != nil {
		return models.User{}, "", fmt.Errorf("get user %s: %w", username, err)
	}
	users := []models.User{u}
	if err := s.identities(ctx, users); err != nil {
		return models.User{}, "", err
	}
	return users[0], hash, nil
}

// UserUpdate changes a user; nil fields stay as they are.
type UserUpdate struct {
	Role         *string
	PasswordHash *string // "" removes the password
	Disabled     *bool
	Identities   map[string]string // nil keeps them, empty removes them
}

// UpdateUser applies upd to the user called username and returns it. A
// disabled user's sessions end.
func (s *Store) UpdateUser(ctx context.Context, username string, upd UserUpdate) (models.User, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return models.User{}, fmt.Errorf("update user %s: %w", username, err)
	}
	defer func() { _ = tx.Rollback(context.WithoutCancel(ctx)) }()

	u, err := scanUser(tx.QueryRow(ctx, `
		UPDATE users AS u SET role = COALESCE($2, role), password_hash = COALESCE($3, password_hash),
			disabled = COALESCE($4, disabled), updated_at = now()
		WHERE username = $1 RETURNING `+userColumns, username, upd.Role, upd.PasswordHash, upd.Disabled))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, fmt.Errorf("%w: %s", ErrUserNotFound, username)
	}
	if err != nil {
		return models.User{}, fmt.Errorf("update user %s: %w", username, err)
	}
	if err := setIdentities(ctx, tx, u.ID, upd.Identities); err != nil {
		return models.User{}, err
	}
	if u.Disabled {
		if _, err := tx.Exec(ctx, "DELETE FROM sessions WHERE user_id = $1", u.ID); err != nil {
			return models.User{}, fmt.Errorf("end sessions of user %s: %w", username, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return models.User{}, fmt.Errorf("update user %s: %w", username, err)
	}
	users := []models.User{u}
	if err := s.identities(ctx, users); err != nil {
		return models.User{}, err
	}
	return users[0], nil
}

// DeleteUser removes a user with its sessions and tokens and reports
// whether it existed.
func (s *Store) DeleteUser(ctx context.Context, username string) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM users WHERE username = $1", username)
	if err != nil {
		return false, fmt.Errorf("delete user %s: %w", username, err)
	}
	return tag.RowsAffected() > 0, nil
}

// UserByIdentity returns the user who signs in as login (with the forge's
// stable id subject) on forge. A login an admin named ahead of the first
// sign-in is bound to subject then; later sign-ins match the subject, so a
// renamed or reused forge login cannot take the account over.
func (s *Store) UserByIdentity(ctx context.Context, forge, subject, login string) (models.User, error) {
	var id int64
	// The bound subject wins over a login named for someone else.
	err := s.pool.QueryRow(ctx, "UPDATE user_identities SET login = $3 WHERE forge = $1 AND subject = $2 RETURNING user_id",
		forge, subject, login).Scan(&id)
	if errors.Is(err, pgx.ErrNoRows) {
		err = s.pool.QueryRow(ctx, "UPDATE user_identities SET subject = $2 WHERE forge = $1 AND subject = '' AND login = $3 RETURNING user_id",
			forge, subject, login).Scan(&id)
	}
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, fmt.Errorf("%w: %s user %s", ErrUserNotFound, forge, login)
	}
	if uniqueViolation(err) {
		return models.User{}, fmt.Errorf("%w: %s login %s is linked to another user", ErrUserNotFound, forge, login)
	}
	if err != nil {
		return models.User{}, fmt.Errorf("find %s user %s: %w", forge, login, err)
	}
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+" FROM users u WHERE u.id = $1 AND NOT u.disabled", id))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, fmt.Errorf("%w: %s user %s is disabled", ErrUserNotFound, forge, login)
	}
	if err != nil {
		return models.User{}, fmt.Errorf("find %s user %s: %w", forge, login, err)
	}
	return u, nil
}

// CreateSession starts a session of userID, kept as digest, until expires.
func (s *Store) CreateSession(ctx context.Context, digest string, userID int64, expires time.Time) error {
	if _, err := s.pool.Exec(ctx, "INSERT INTO sessions (digest, user_id, expires_at) VALUES ($1, $2, $3)",
		digest, userID, expires); err != nil {
		return fmt.Errorf("create session of user %d: %w", userID, err)
	}
	return nil
}

// SessionUser returns the user of the session kept as digest, if it has
// not expired and the user is not disabled.
func (s *Store) SessionUser(ctx context.Context, digest string) (models.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, "SELECT "+userColumns+` FROM sessions JOIN users u ON u.id = sessions.user_id
		WHERE sessions.digest = $1 AND sessions.expires_at > now() AND NOT u.disabled`, digest))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, ErrUserNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("find session: %w", err)
	}
	return u, nil
}

// DeleteSession ends the session kept as digest.
func (s *Store) DeleteSession(ctx context.Context, digest string) error {
	if _, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE digest = $1", digest); err != nil {
		return fmt.Errorf("delete session: %w", err)
	}
	return nil
}

// PruneSessions forgets expired sessions and returns how many it removed.
func (s *Store) PruneSessions(ctx context.Context) (int, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM sessions WHERE expires_at <= now()")
	if err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// CreateAPIToken stores a personal token of userID, kept as digest.
func (s *Store) CreateAPIToken(ctx context.Context, userID int64, name, digest string) (models.APIToken, error) {
	var t models.APIToken
	err := s.pool.QueryRow(ctx, "INSERT INTO api_tokens (user_id, name, digest) VALUES ($1, $2, $3) RETURNING id, name, created_at, last_used_at",
		userID, name, digest).Scan(&t.ID, &t.Name, &t.CreatedAt, &t.LastUsedAt)
	if err != nil {
		return models.APIToken{}, fmt.Errorf("create token of user %d: %w", userID, err)
	}
	return t, nil
}

// APITokens lists the personal tokens of userID, newest first.
func (s *Store) APITokens(ctx context.Context, userID int64) ([]models.APIToken, error) {
	rows, err := s.pool.Query(ctx, "SELECT id, name, created_at, last_used_at FROM api_tokens WHERE user_id = $1 ORDER BY id DESC", userID)
	if err != nil {
		return nil, fmt.Errorf("list tokens of user %d: %w", userID, err)
	}
	tokens, err := pgx.CollectRows(rows, pgx.RowToStructByPos[models.APIToken])
	if err != nil {
		return nil, fmt.Errorf("list tokens of user %d: %w", userID, err)
	}
	return tokens, nil
}

// DeleteAPIToken revokes token id of userID and reports whether it existed.
func (s *Store) DeleteAPIToken(ctx context.Context, userID, id int64) (bool, error) {
	tag, err := s.pool.Exec(ctx, "DELETE FROM api_tokens WHERE id = $1 AND user_id = $2", id, userID)
	if err != nil {
		return false, fmt.Errorf("delete token %d: %w", id, err)
	}
	return tag.RowsAffected() > 0, nil
}

// TokenUser returns the user of the personal token kept as digest, unless
// the user is disabled, and records the token's use.
func (s *Store) TokenUser(ctx context.Context, digest string) (models.User, error) {
	u, err := scanUser(s.pool.QueryRow(ctx, `
		WITH used AS (UPDATE api_tokens SET last_used_at = now() WHERE digest = $1 RETURNING user_id)
		SELECT `+userColumns+` FROM used JOIN users u ON u.id = used.user_id WHERE NOT u.disabled`, digest))
	if errors.Is(err, pgx.ErrNoRows) {
		return models.User{}, ErrUserNotFound
	}
	if err != nil {
		return models.User{}, fmt.Errorf("find token: %w", err)
	}
	return u, nil
}
