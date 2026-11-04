package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

func (m *memAccounts) RegisterUser(ctx context.Context, username, hash, role string) (models.User, bool, error) {
	first := len(m.users) == 0
	if first {
		role = string(auth.RoleAdmin)
	}
	if role == "" {
		return models.User{}, false, db.ErrSignupClosed
	}
	u, err := m.CreateUser(ctx, models.User{Username: username, Role: role}, hash)
	return u, first, err
}

func signupRouter(m *memAccounts, role auth.Role) http.Handler {
	access := NewAccess(AccessConfig{PublicRead: true, Accounts: m, SignupRole: role, Log: slog.New(slog.DiscardHandler)})
	return NewRouter(Handlers{Agents: http.NotFoundHandler(), Access: access, DB: &mockPinger{}}, slog.New(slog.DiscardHandler))
}

func register(h http.Handler, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, "/api/register", strings.NewReader(body))
	return send(h, req)
}

func meOf(t *testing.T, h http.Handler, c *http.Cookie) meResponse {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/me", nil)
	if c != nil {
		req.AddCookie(c)
	}
	rec := send(h, req)
	require.Equal(t, http.StatusOK, rec.Code)
	var me meResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	return me
}

func TestFirstSignUpIsAnAdmin(t *testing.T) {
	m := newMemAccounts()
	h := signupRouter(m, "")
	assert.Equal(t, "first", meOf(t, h, nil).Signup, "a server with no users takes its first account")

	rec := register(h, `{"username":" Ada ","password":"`+password+`"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var u models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &u))
	assert.Equal(t, "ada", u.Username, "trimmed and lowercased, as sign-in does")
	assert.Equal(t, "admin", u.Role)

	var session *http.Cookie
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			session = c
		}
	}
	require.NotNil(t, session, "signing up signs in")
	me := meOf(t, h, session)
	require.NotNil(t, me.User)
	assert.Equal(t, "ada", me.User.Username)
	assert.Equal(t, "session", me.Via)
	assert.Empty(t, meOf(t, h, nil).Signup, "after the first account, sign-up is closed")

	rec = register(h, `{"username":"eve","password":"`+password+`"}`)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "ask an admin")
}

func TestOpenSignUp(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "root", "admin")
	h := signupRouter(m, auth.RoleViewer)
	assert.Equal(t, "open", meOf(t, h, nil).Signup)

	rec := register(h, `{"username":"bob","password":"`+password+`"}`)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"role":"viewer"`)

	for body, want := range map[string]int{
		`{"username":"bob","password":"` + password + `"}`:   http.StatusConflict,
		`{"username":"b o b","password":"` + password + `"}`: http.StatusBadRequest,
		`{"username":"carl","password":"short"}`:             http.StatusBadRequest,
		`{"username":"carl","password":"x","role":"admin"}`:  http.StatusBadRequest,
		`not json`: http.StatusBadRequest,
	} {
		assert.Equal(t, want, register(h, body).Code, body)
	}
	_, _, err := m.User(t.Context(), "carl")
	assert.ErrorIs(t, err, db.ErrUserNotFound)
}

func TestProviderSignUpIsAnnounced(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "root", "admin")
	access := NewAccess(AccessConfig{PublicRead: true, Accounts: m, Log: slog.New(slog.DiscardHandler),
		OAuth: OAuthConfig{Providers: []*auth.Provider{auth.GiteaProvider("gitea", "https://git.example.com", "id", "secret")},
			PublicURL: "https://ci.example.com", SignupRole: auth.RoleViewer}})
	h := NewRouter(Handlers{Agents: http.NotFoundHandler(), Access: access, DB: &mockPinger{}}, slog.New(slog.DiscardHandler))
	me := meOf(t, h, nil)
	assert.True(t, me.ProviderSignup)
	assert.Equal(t, []string{"gitea"}, me.Providers)
	assert.Empty(t, me.Signup, "passwords still need an admin")
}
