package api

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

const (
	// sessionCookie keeps a signed-in user's session; the server stores
	// only its digest.
	sessionCookie = "cisid_session"
	// tokenCookie keeps a browser signed in for reading with the read or
	// API token. It holds a digest of the token, never the token, and only
	// ever grants reading.
	tokenCookie = "cisid_token"
	// requestHeader must come with changes authenticated by a cookie. A
	// cross-site form cannot set it, so it stops cross-site request
	// forgery on top of the cookies' SameSite.
	requestHeader = "X-Cisid-Request"

	// DefaultSessionTTL is how long a sign-in lasts.
	DefaultSessionTTL = 7 * 24 * time.Hour
)

// AccountStore keeps users, their sessions and tokens. *db.Store
// satisfies it.
type AccountStore interface {
	CountUsers(ctx context.Context) (int, error)
	User(ctx context.Context, username string) (models.User, string, error)
	Users(ctx context.Context) ([]models.User, error)
	CreateUser(ctx context.Context, u models.User, passwordHash string) (models.User, error)
	UpdateUser(ctx context.Context, username string, upd db.UserUpdate) (models.User, error)
	DeleteUser(ctx context.Context, username string) (bool, error)
	RegisterUser(ctx context.Context, username, passwordHash, role string) (models.User, bool, error)
	UserByIdentity(ctx context.Context, forge, subject, login string) (models.User, error)
	CreateSession(ctx context.Context, digest string, userID int64, expires time.Time) error
	SessionUser(ctx context.Context, digest string) (models.User, error)
	DeleteSession(ctx context.Context, digest string) error
	CreateAPIToken(ctx context.Context, userID int64, name, digest string) (models.APIToken, error)
	APITokens(ctx context.Context, userID int64) ([]models.APIToken, error)
	DeleteAPIToken(ctx context.Context, userID, id int64) (bool, error)
	TokenUser(ctx context.Context, digest string) (models.User, error)
}

var _ AccountStore = (*db.Store)(nil)

// AccessConfig says who may do what.
type AccessConfig struct {
	// APIToken may do everything (CISID_API_TOKEN).
	APIToken string
	// ReadToken may read (CISID_READ_TOKEN).
	ReadToken string
	// PublicRead lets anyone read jobs, logs and metrics.
	PublicRead bool
	// Accounts enables users; nil leaves only the tokens above.
	Accounts AccountStore
	// SessionTTL is how long a sign-in lasts; DefaultSessionTTL when 0.
	SessionTTL time.Duration
	// OAuth signs users in through their forges; see oauth.go.
	OAuth OAuthConfig
	// SignupRole lets anyone open an account with a password, getting this
	// role; empty takes no sign-ups, except the first account of a server
	// with no users, which becomes an admin.
	SignupRole auth.Role
	// ForgeAccounts keeps users' own access to their forges; nil when the
	// server has no CISID_SECRET_KEY to seal it with.
	ForgeAccounts ForgeAccounts
	Log           *slog.Logger
}

// Access decides who may do what. A caller is known by
//
//   - "Authorization: Bearer <token>": the API token (admin), the read
//     token (viewer) or a user's personal token (the user's role);
//   - the session cookie that signing in sets;
//   - the read cookie that signing in with the read or API token sets,
//     which only reads.
//
// Log streams rely on the cookies: an EventSource cannot send headers.
type Access struct {
	cfg AccessConfig
	// someUsers is set once the server is known to have a user.
	someUsers atomic.Bool
	digests   []string // of the tokens, as the read cookie holds them
	log       *slog.Logger
	now       func() time.Time
}

// NewAccess returns the access rules cfg describes.
func NewAccess(cfg AccessConfig) *Access {
	if cfg.SessionTTL <= 0 {
		cfg.SessionTTL = DefaultSessionTTL
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	a := &Access{cfg: cfg, log: cfg.Log, now: time.Now}
	for _, t := range []string{cfg.ReadToken, cfg.APIToken} {
		if t != "" {
			a.digests = append(a.digests, cookieDigest(t))
		}
	}
	return a
}

// openAccess lets anyone read and no one change anything: the default of
// the APIs when they get no Access.
func openAccess() *Access {
	return NewAccess(AccessConfig{PublicRead: true})
}

// principal is who made a request.
type principal struct {
	user   *models.User // nil for the server's own tokens
	role   auth.Role
	cookie bool   // signed in by a cookie
	name   string // for logs
}

type principalKey struct{}

// caller returns who made the request, as the access check found.
func caller(r *http.Request) (principal, bool) {
	p, ok := r.Context().Value(principalKey{}).(principal)
	return p, ok
}

func cookieDigest(token string) string {
	sum := sha256.Sum256([]byte("cisid read access\x00" + token))
	return hex.EncodeToString(sum[:])
}

// equal compares two secrets in constant time; an empty want matches
// nothing.
func equal(got, want string) bool {
	return want != "" && subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// who identifies the caller. A bearer token that is not accepted does not
// fall back to cookies.
func (a *Access) who(r *http.Request) (principal, bool) {
	ctx := r.Context()
	if got, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok {
		switch {
		case equal(got, a.cfg.APIToken):
			return principal{role: auth.RoleAdmin, name: "API token"}, true
		case equal(got, a.cfg.ReadToken):
			return principal{role: auth.RoleViewer, name: "read token"}, true
		case a.cfg.Accounts != nil && strings.HasPrefix(got, auth.TokenPrefix):
			u, err := a.cfg.Accounts.TokenUser(ctx, auth.Digest(got))
			if err != nil {
				a.lookupFailed(ctx, "token", err)
				return principal{}, false
			}
			return userPrincipal(u, false), true
		}
		return principal{}, false
	}
	if c, err := r.Cookie(sessionCookie); err == nil && a.cfg.Accounts != nil {
		u, err := a.cfg.Accounts.SessionUser(ctx, auth.Digest(c.Value))
		if err == nil {
			return userPrincipal(u, true), true
		}
		a.lookupFailed(ctx, "session", err)
	}
	if c, err := r.Cookie(tokenCookie); err == nil {
		for _, d := range a.digests {
			if equal(c.Value, d) {
				return principal{role: auth.RoleViewer, cookie: true, name: "read cookie"}, true
			}
		}
	}
	return principal{}, false
}

func userPrincipal(u models.User, cookie bool) principal {
	return principal{user: &u, role: auth.Role(u.Role), cookie: cookie, name: u.Username}
}

// lookupFailed logs a failure to look a session or token up, but not
// one that simply does not exist.
func (a *Access) lookupFailed(ctx context.Context, what string, err error) {
	if !errors.Is(err, db.ErrUserNotFound) {
		a.log.ErrorContext(ctx, "look "+what+" up", "error", err)
	}
}

// require lets only callers with role need reach next. Reading is open
// to anyone when PublicRead is set.
func (a *Access) require(need auth.Role, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		p, ok := a.who(r)
		switch {
		case !ok && need == auth.RoleViewer && a.cfg.PublicRead:
			next(w, r)
			return
		case !ok:
			a.refuse(w, r, need)
			return
		case !p.role.Allows(need):
			writeError(w, http.StatusForbidden, "this needs the "+string(need)+" role")
			return
		case p.cookie && r.Method != http.MethodGet && r.Method != http.MethodHead && r.Header.Get(requestHeader) == "":
			writeError(w, http.StatusForbidden, "changes signed in by a cookie must send the "+requestHeader+" header")
			return
		}
		next(w, r.WithContext(context.WithValue(r.Context(), principalKey{}, p)))
	}
}

// refuse answers a caller who did not authenticate.
func (a *Access) refuse(w http.ResponseWriter, r *http.Request, need auth.Role) {
	if need != auth.RoleViewer && a.cfg.APIToken == "" && !a.hasUsers(r.Context()) {
		writeError(w, http.StatusForbidden, "write API disabled: set CISID_API_TOKEN or create a user")
		return
	}
	w.Header().Set("WWW-Authenticate", `Bearer realm="cisid"`)
	if need == auth.RoleViewer {
		writeError(w, http.StatusUnauthorized, "sign in or send the read token")
		return
	}
	writeError(w, http.StatusUnauthorized, "sign in or send an API token")
}

func (a *Access) hasUsers(ctx context.Context) bool {
	if a.cfg.Accounts == nil {
		return false
	}
	n, err := a.cfg.Accounts.CountUsers(ctx)
	if err != nil {
		a.log.ErrorContext(ctx, "count users", "error", err)
		return false
	}
	return n > 0
}

// Wrap lets only callers who may read reach next.
func (a *Access) Wrap(next http.Handler) http.Handler {
	return a.require(auth.RoleViewer, next.ServeHTTP)
}

func (a *Access) wrapFunc(next http.HandlerFunc) http.HandlerFunc {
	return a.require(auth.RoleViewer, next)
}

// secureRequest reports whether the browser reached the server over HTTPS,
// directly or through a proxy that says so.
func secureRequest(r *http.Request) bool {
	return r.TLS != nil || strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}
