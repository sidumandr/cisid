// Package forgeauth keeps users' own access to their forges, connected
// by OAuth or with a token of their own, sealed with the server's secret
// key. The repositories a user connects are reached with it: polled,
// cloned and reported on with their access rather than the server's.
package forgeauth

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/internal/forge"
	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// ErrNotConnected is returned for a forge the user has not connected.
var ErrNotConnected = errors.New("forge not connected")

// ErrTokenRejected is returned when the forge does not take a token.
var ErrTokenRejected = errors.New("the forge does not take this token")

// The ways an account is connected.
const (
	KindOAuth = "oauth"
	KindToken = "token"
)

// refreshAhead renews an expiring token this long before it expires.
const refreshAhead = 2 * time.Minute

// Store keeps the sealed accounts. *db.Store satisfies it.
type Store interface {
	PutForgeAccount(ctx context.Context, a db.SealedForgeAccount) error
	SealedForgeAccount(ctx context.Context, userID int64, forge string) (db.SealedForgeAccount, error)
	ForgeAccounts(ctx context.Context, userID int64) ([]models.ForgeAccount, error)
	DeleteForgeAccount(ctx context.Context, userID int64, forge string) (bool, error)
	RepositoryUser(ctx context.Context, repo string) (int64, bool, error)
}

var _ Store = (*db.Store)(nil)

// Forges says whose a token is, and which forges take users' own tokens.
// *forge.Set satisfies it.
type Forges interface {
	Whoami(ctx context.Context, name, token string) (string, error)
	Accounts() []string
}

var _ Forges = (*forge.Set)(nil)

// Refresher renews a forge's OAuth token from its refresh token.
type Refresher func(ctx context.Context, refreshToken string) (auth.Token, error)

// Service connects users' forge accounts and hands out their tokens.
type Service struct {
	store      Store
	cipher     *secrets.Cipher
	forges     Forges
	refreshers map[string]Refresher
	log        *slog.Logger
	now        func() time.Time
}

// New returns a Service. refreshers renew the OAuth tokens of the forges
// whose tokens expire, by forge name.
func New(store Store, cipher *secrets.Cipher, forges Forges, refreshers map[string]Refresher, log *slog.Logger) *Service {
	if log == nil {
		log = slog.Default()
	}
	return &Service{store: store, cipher: cipher, forges: forges, refreshers: refreshers, log: log, now: time.Now}
}

// Forges names the forges users can connect.
func (s *Service) Forges() []string { return s.forges.Accounts() }

// Accounts lists the forges userID connected.
func (s *Service) Accounts(ctx context.Context, userID int64) ([]models.ForgeAccount, error) {
	list, err := s.store.ForgeAccounts(ctx, userID)
	if err != nil {
		return nil, err
	}
	if list == nil {
		list = []models.ForgeAccount{}
	}
	return list, nil
}

// ConnectToken connects forge for userID with a token of their own, once
// the forge says whose it is.
func (s *Service) ConnectToken(ctx context.Context, userID int64, forgeName, token string) (models.ForgeAccount, error) {
	return s.connect(ctx, userID, forgeName, KindToken, auth.Token{AccessToken: token})
}

// ConnectOAuth connects forge for userID with the token an OAuth sign-in
// on the forge gave.
func (s *Service) ConnectOAuth(ctx context.Context, userID int64, forgeName string, tok auth.Token) (models.ForgeAccount, error) {
	return s.connect(ctx, userID, forgeName, KindOAuth, tok)
}

func (s *Service) connect(ctx context.Context, userID int64, forgeName, kind string, tok auth.Token) (models.ForgeAccount, error) {
	login, err := s.forges.Whoami(ctx, forgeName, tok.AccessToken)
	switch {
	case errors.Is(err, forge.ErrUnknownForge), errors.Is(err, forge.ErrNoAccounts), errors.Is(err, forge.ErrTemporary):
		return models.ForgeAccount{}, err
	case err != nil:
		// A forge refuses a token it does not take with 401 or 403.
		return models.ForgeAccount{}, fmt.Errorf("%w: %w", ErrTokenRejected, err)
	case login == "":
		return models.ForgeAccount{}, fmt.Errorf("%w: %s did not say whose it is", ErrTokenRejected, forgeName)
	}
	sealed, err := s.seal(userID, forgeName, login, kind, tok)
	if err != nil {
		return models.ForgeAccount{}, err
	}
	if err := s.store.PutForgeAccount(ctx, sealed); err != nil {
		return models.ForgeAccount{}, err
	}
	s.log.InfoContext(ctx, "forge account connected", "user_id", userID, "forge", forgeName, "login", login, "kind", kind)
	return models.ForgeAccount{Forge: forgeName, Login: login, Kind: kind, CreatedAt: s.now()}, nil
}

// Disconnect forgets userID's access to forge.
func (s *Service) Disconnect(ctx context.Context, userID int64, forgeName string) (bool, error) {
	return s.store.DeleteForgeAccount(ctx, userID, forgeName)
}

// Token returns userID's token for forge, renewing it first when it is
// about to expire. ErrNotConnected when they have not connected it.
func (s *Service) Token(ctx context.Context, userID int64, forgeName string) (string, error) {
	a, err := s.store.SealedForgeAccount(ctx, userID, forgeName)
	if errors.Is(err, db.ErrForgeAccountNotFound) {
		return "", fmt.Errorf("%w: %s", ErrNotConnected, forgeName)
	}
	if err != nil {
		return "", err
	}
	if a.ExpiresAt != nil && a.Refresh != nil && s.now().Add(refreshAhead).After(*a.ExpiresAt) {
		return s.renew(ctx, a)
	}
	return s.cipher.Open(owner(userID), tokenName(forgeName), a.Token)
}

// renew trades the account's refresh token for a new token and keeps it.
func (s *Service) renew(ctx context.Context, a db.SealedForgeAccount) (string, error) {
	refresh, ok := s.refreshers[a.Forge]
	if !ok {
		return "", fmt.Errorf("renew %s token of user %d: no OAuth app to renew it with", a.Forge, a.UserID)
	}
	old, err := s.cipher.Open(owner(a.UserID), refreshName(a.Forge), a.Refresh)
	if err != nil {
		return "", err
	}
	tok, err := refresh(ctx, old)
	if err != nil {
		return "", fmt.Errorf("renew %s token of user %d: %w", a.Forge, a.UserID, err)
	}
	if tok.RefreshToken == "" {
		tok.RefreshToken = old
	}
	sealed, err := s.seal(a.UserID, a.Forge, a.Login, a.Kind, tok)
	if err != nil {
		return "", err
	}
	if err := s.store.PutForgeAccount(ctx, sealed); err != nil {
		return "", err
	}
	return tok.AccessToken, nil
}

// RepositoryToken is the token to reach the connected repository key
// with: the access of the user who connected it, if they connected its
// forge. It fits forge.Set.UseCredentials; a failure is logged, and the
// server's token is used instead.
func (s *Service) RepositoryToken(ctx context.Context, key string) (string, bool) {
	userID, ok, err := s.store.RepositoryUser(ctx, key)
	if err != nil {
		s.log.ErrorContext(ctx, "find whose access reaches a repository", "repo", key, "error", err)
		return "", false
	}
	if !ok {
		return "", false
	}
	forgeName, _, err := forge.SplitKey(key)
	if err != nil {
		return "", false
	}
	token, err := s.Token(ctx, userID, forgeName)
	if errors.Is(err, ErrNotConnected) {
		return "", false
	}
	if err != nil {
		s.log.WarnContext(ctx, "user's forge token unusable, using the server's", "repo", key, "user_id", userID, "error", err)
		return "", false
	}
	return token, true
}

func (s *Service) seal(userID int64, forgeName, login, kind string, tok auth.Token) (db.SealedForgeAccount, error) {
	sealed, err := s.cipher.Seal(owner(userID), tokenName(forgeName), tok.AccessToken)
	if err != nil {
		return db.SealedForgeAccount{}, fmt.Errorf("seal %s token: %w", forgeName, err)
	}
	a := db.SealedForgeAccount{UserID: userID, Forge: forgeName, Login: login, Kind: kind, Token: sealed}
	if tok.RefreshToken != "" {
		if a.Refresh, err = s.cipher.Seal(owner(userID), refreshName(forgeName), tok.RefreshToken); err != nil {
			return db.SealedForgeAccount{}, fmt.Errorf("seal %s refresh token: %w", forgeName, err)
		}
	}
	if !tok.Expiry.IsZero() {
		exp := tok.Expiry
		a.ExpiresAt = &exp
	}
	return a, nil
}

// owner, tokenName and refreshName bind each sealed token to its user and
// forge, so one moved to another row does not open.
func owner(userID int64) string { return "user " + strconv.FormatInt(userID, 10) }

func tokenName(forgeName string) string { return "forge token " + forgeName }

func refreshName(forgeName string) string { return "forge refresh " + forgeName }
