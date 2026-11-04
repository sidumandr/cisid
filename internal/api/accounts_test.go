package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/auth"
	"github.com/sidumandr/cisid/internal/db"
	"github.com/sidumandr/cisid/pkg/models"
)

// memAccounts is an in-memory AccountStore.
type memAccounts struct {
	mu       sync.Mutex
	users    map[string]models.User
	hashes   map[string]string
	subjects map[string]string // "forge login" -> subject
	sessions map[string]int64  // digest -> user id
	tokens   map[string]int64  // digest -> user id
	names    map[int64]models.APIToken
	nextID   int64
}

func newMemAccounts() *memAccounts {
	return &memAccounts{users: map[string]models.User{}, hashes: map[string]string{}, subjects: map[string]string{},
		sessions: map[string]int64{}, tokens: map[string]int64{}, names: map[int64]models.APIToken{}}
}

func (m *memAccounts) byID(id int64) (models.User, bool) {
	for _, u := range m.users {
		if u.ID == id {
			return u, true
		}
	}
	return models.User{}, false
}

func (m *memAccounts) CountUsers(context.Context) (int, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.users), nil
}

func (m *memAccounts) User(_ context.Context, name string) (models.User, string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[name]
	if !ok {
		return models.User{}, "", db.ErrUserNotFound
	}
	return u, m.hashes[name], nil
}

func (m *memAccounts) Users(context.Context) ([]models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.User
	for _, u := range m.users {
		out = append(out, u)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Username < out[j].Username })
	return out, nil
}

func (m *memAccounts) CreateUser(_ context.Context, u models.User, hash string) (models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.users[u.Username]; ok {
		return models.User{}, db.ErrUserExists
	}
	m.nextID++
	u.ID, u.HasPassword, u.CreatedAt = m.nextID, hash != "", time.Unix(0, 0).UTC()
	m.users[u.Username], m.hashes[u.Username] = u, hash
	return u, nil
}

func (m *memAccounts) UpdateUser(_ context.Context, name string, upd db.UserUpdate) (models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	u, ok := m.users[name]
	if !ok {
		return models.User{}, db.ErrUserNotFound
	}
	if upd.Role != nil {
		u.Role = *upd.Role
	}
	if upd.PasswordHash != nil {
		m.hashes[name], u.HasPassword = *upd.PasswordHash, *upd.PasswordHash != ""
	}
	if upd.Disabled != nil {
		u.Disabled = *upd.Disabled
	}
	if upd.Identities != nil {
		u.Identities = upd.Identities
	}
	m.users[name] = u
	return u, nil
}

func (m *memAccounts) DeleteUser(_ context.Context, name string) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.users[name]
	delete(m.users, name)
	return ok, nil
}

func (m *memAccounts) UserByIdentity(_ context.Context, forge, subject, login string) (models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, u := range m.users {
		if u.Identities[forge] == login && !u.Disabled {
			if s, bound := m.subjects[forge+" "+login]; bound && s != subject {
				break
			}
			m.subjects[forge+" "+login] = subject
			return u, nil
		}
	}
	return models.User{}, db.ErrUserNotFound
}

func (m *memAccounts) CreateSession(_ context.Context, digest string, id int64, _ time.Time) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[digest] = id
	return nil
}

func (m *memAccounts) SessionUser(_ context.Context, digest string) (models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if u, ok := m.byID(m.sessions[digest]); ok && !u.Disabled {
		return u, nil
	}
	return models.User{}, db.ErrUserNotFound
}

func (m *memAccounts) DeleteSession(_ context.Context, digest string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, digest)
	return nil
}

func (m *memAccounts) CreateAPIToken(_ context.Context, id int64, name, digest string) (models.APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.nextID++
	t := models.APIToken{ID: m.nextID, Name: name}
	m.tokens[digest], m.names[t.ID] = id, t
	return t, nil
}

func (m *memAccounts) APITokens(_ context.Context, id int64) ([]models.APIToken, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.APIToken
	for _, uid := range m.tokens {
		if uid == id {
			for _, t := range m.names {
				out = append(out, t)
			}
			break
		}
	}
	return out, nil
}

func (m *memAccounts) DeleteAPIToken(_ context.Context, id, tokenID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.names[tokenID]; !ok {
		return false, nil
	}
	for d, uid := range m.tokens {
		if uid == id {
			delete(m.tokens, d)
			delete(m.names, tokenID)
			return true, nil
		}
	}
	return false, nil
}

func (m *memAccounts) TokenUser(_ context.Context, digest string) (models.User, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	id, ok := m.tokens[digest]
	if u, found := m.byID(id); ok && found && !u.Disabled {
		return u, nil
	}
	return models.User{}, db.ErrUserNotFound
}

const password = "correct horse battery"

func addUser(t *testing.T, m *memAccounts, name, role string) {
	t.Helper()
	hash, err := auth.HashPasswordWith(password, 1) // fast; checking reads the count from the hash
	require.NoError(t, err)
	_, err = m.CreateUser(t.Context(), models.User{Username: name, Role: role}, hash)
	require.NoError(t, err)
}

func accountsRouter(t *testing.T, m *memAccounts, publicRead bool) (http.Handler, *mockJobReader) {
	t.Helper()
	access := NewAccess(AccessConfig{PublicRead: publicRead, Accounts: m, Log: slog.New(slog.DiscardHandler)})
	store := &mockJobReader{}
	store.On("ListJobs", mock.Anything, mock.Anything).Return([]models.Job{}, nil)
	store.On("CancelJob", mock.Anything, mock.Anything).Return(models.Job{ID: 1, Status: models.JobStatusCanceled}, nil)
	jobs := NewJobsAPI(store, &fakeEvents{}, slog.New(slog.DiscardHandler), WithAccess(access))
	t.Cleanup(jobs.Close)
	return NewRouter(Handlers{Agents: http.NotFoundHandler(), Jobs: jobs, Access: access, DB: &mockPinger{}},
		slog.New(slog.DiscardHandler)), store
}

// signIn signs name in with a password and returns the session cookie.
func signIn(t *testing.T, h http.Handler, name string) *http.Cookie {
	t.Helper()
	req := httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"`+name+`","password":"`+password+`"}`))
	req.Header.Set("X-Forwarded-Proto", "https")
	rec := send(h, req)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	for _, c := range rec.Result().Cookies() {
		if c.Name == sessionCookie {
			return c
		}
	}
	t.Fatal("no session cookie")
	return nil
}

// as sends a request with the session cookie; ui adds the header the UI
// sends with changes.
func as(h http.Handler, c *http.Cookie, method, target, body string, ui bool) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if c != nil {
		req.AddCookie(c)
	}
	if ui {
		req.Header.Set(requestHeader, "1")
	}
	return send(h, req)
}

func TestPasswordSignIn(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ahmet", "maintainer")
	h, store := accountsRouter(t, m, false)

	for _, body := range []string{
		`{"username":"ahmet","password":"wrong password"}`,
		`{"username":"nobody","password":"` + password + `"}`,
	} {
		rec := send(h, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(body)))
		assert.Equal(t, http.StatusUnauthorized, rec.Code)
		assert.JSONEq(t, `{"status":"error","reason":"wrong username or password"}`, rec.Body.String(),
			"an unknown user and a wrong password look the same")
	}

	c := signIn(t, h, "Ahmet ")
	assert.True(t, c.HttpOnly)
	assert.True(t, c.Secure, "behind HTTPS")
	assert.Equal(t, http.SameSiteLaxMode, c.SameSite)
	assert.Positive(t, c.MaxAge)

	rec := as(h, c, http.MethodGet, "/api/me", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	var me meResponse
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &me))
	assert.Equal(t, "ahmet", me.User.Username)
	assert.Equal(t, "maintainer", me.Role)
	assert.Equal(t, "session", me.Via)
	assert.False(t, me.PublicRead)
	assert.True(t, me.Accounts)

	assert.Equal(t, http.StatusOK, as(h, c, http.MethodGet, "/api/jobs", "", false).Code)
	assert.Equal(t, http.StatusUnauthorized, as(h, nil, http.MethodGet, "/api/jobs", "", false).Code, "reading needs a sign-in")

	rec = as(h, c, http.MethodPost, "/api/jobs/1/cancel", "", false)
	assert.Equal(t, http.StatusForbidden, rec.Code, "a cookie change without the UI's header may be forged")
	store.AssertNotCalled(t, "CancelJob", mock.Anything, mock.Anything)
	assert.Equal(t, http.StatusOK, as(h, c, http.MethodPost, "/api/jobs/1/cancel", "", true).Code)

	rec = as(h, c, http.MethodPost, "/api/logout", "", false)
	assert.Equal(t, http.StatusNoContent, rec.Code)
	assert.Equal(t, http.StatusUnauthorized, as(h, c, http.MethodGet, "/api/jobs", "", false).Code, "logout ends the session")

	disabled := true
	_, err := m.UpdateUser(t.Context(), "ahmet", db.UserUpdate{Disabled: &disabled})
	require.NoError(t, err)
	rec = send(h, httptest.NewRequest(http.MethodPost, "/api/login", strings.NewReader(`{"username":"ahmet","password":"`+password+`"}`)))
	assert.Equal(t, http.StatusUnauthorized, rec.Code, "a disabled user cannot sign in")
}

func TestRolesLimitWhatUsersDo(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ayse", "viewer")
	h, _ := accountsRouter(t, m, true)
	c := signIn(t, h, "ayse")

	assert.Equal(t, http.StatusOK, as(h, nil, http.MethodGet, "/api/jobs", "", false).Code, "reading is public here")
	rec := as(h, c, http.MethodPost, "/api/jobs/1/cancel", "", true)
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "maintainer role")
	assert.Equal(t, http.StatusForbidden, as(h, c, http.MethodGet, "/api/users", "", false).Code, "only admins see users")
	assert.Equal(t, http.StatusUnauthorized, as(h, nil, http.MethodPost, "/api/jobs/1/cancel", "", false).Code)
}

func TestPersonalTokens(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ahmet", "maintainer")
	h, _ := accountsRouter(t, m, false)
	c := signIn(t, h, "ahmet")

	assert.Equal(t, http.StatusBadRequest, as(h, c, http.MethodPost, "/api/me/tokens", `{"name":""}`, true).Code)
	rec := as(h, c, http.MethodPost, "/api/me/tokens", `{"name":"deploy script"}`, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	var created struct {
		ID    int64  `json:"id"`
		Name  string `json:"name"`
		Token string `json:"token"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &created))
	assert.True(t, strings.HasPrefix(created.Token, auth.TokenPrefix))

	assert.Equal(t, http.StatusOK, post(t, h, "/api/jobs/1/cancel", created.Token).Code, "the token acts with the user's role")
	assert.Equal(t, http.StatusUnauthorized, post(t, h, "/api/jobs/1/cancel", created.Token+"x").Code)

	rec = as(h, c, http.MethodGet, "/api/me/tokens", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), created.Token, "a token is shown once")

	assert.Equal(t, http.StatusNoContent, as(h, c, http.MethodDelete, "/api/me/tokens/"+strconv.FormatInt(created.ID, 10), "", true).Code)
	assert.Equal(t, http.StatusUnauthorized, post(t, h, "/api/jobs/1/cancel", created.Token).Code, "a revoked token stops working")
}

func TestChangePassword(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "ahmet", "viewer")
	h, _ := accountsRouter(t, m, false)
	c := signIn(t, h, "ahmet")

	assert.Equal(t, http.StatusForbidden, as(h, c, http.MethodPut, "/api/me/password", `{"current":"nope","new":"another long one"}`, true).Code)
	assert.Equal(t, http.StatusBadRequest, as(h, c, http.MethodPut, "/api/me/password", `{"current":"`+password+`","new":"short"}`, true).Code)
	assert.Equal(t, http.StatusNoContent, as(h, c, http.MethodPut, "/api/me/password", `{"current":"`+password+`","new":"another long one"}`, true).Code)
	_, hash, _ := m.User(t.Context(), "ahmet")
	assert.True(t, auth.CheckPassword(hash, "another long one"))
}

func TestUsersAPI(t *testing.T) {
	m := newMemAccounts()
	addUser(t, m, "root", "admin")
	h, _ := accountsRouter(t, m, false)
	c := signIn(t, h, "root")

	rec := as(h, c, http.MethodPost, "/api/users", `{"username":"ahmet","role":"maintainer","password":"`+password+`","identities":{"gitea":"ahmet.y"}}`, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"has_password":true`)
	assert.NotContains(t, rec.Body.String(), "pbkdf2", "hashes never leave the server")

	for name, body := range map[string]string{
		"bad role":     `{"username":"x","role":"owner"}`,
		"no role":      `{"username":"x"}`,
		"bad username": `{"username":"Ahmet Y","role":"viewer"}`,
		"weak":         `{"username":"x","role":"viewer","password":"short"}`,
		"bad forge":    `{"username":"x","role":"viewer","identities":{"svn":"x"}}`,
		"not an email": `{"username":"x","role":"viewer","identities":{"google":"ahmet"}}`,
	} {
		assert.Equal(t, http.StatusBadRequest, as(h, c, http.MethodPost, "/api/users", body, true).Code, name)
	}
	assert.Equal(t, http.StatusConflict, as(h, c, http.MethodPost, "/api/users", `{"username":"ahmet","role":"viewer"}`, true).Code)

	rec = as(h, c, http.MethodPost, "/api/users", `{"username":"ayse","role":"viewer","identities":{"google":"Ayse@Example.com"}}`, true)
	require.Equal(t, http.StatusCreated, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"google":"ayse@example.com"`, "Google logins are kept as Google sends them")
	require.Equal(t, http.StatusNoContent, as(h, c, http.MethodDelete, "/api/users/ayse", "", true).Code)

	rec = as(h, c, http.MethodPatch, "/api/users/ahmet", `{"role":"viewer","disabled":true}`, true)
	require.Equal(t, http.StatusOK, rec.Code, rec.Body.String())
	assert.Contains(t, rec.Body.String(), `"role":"viewer"`)
	assert.Equal(t, http.StatusNotFound, as(h, c, http.MethodPatch, "/api/users/nobody", `{"role":"viewer"}`, true).Code)

	for _, req := range []struct{ method, body string }{
		{http.MethodPatch, `{"role":"viewer"}`},
		{http.MethodPatch, `{"disabled":true}`},
		{http.MethodDelete, ""},
	} {
		assert.Equal(t, http.StatusForbidden, as(h, c, req.method, "/api/users/root", req.body, true).Code,
			"an admin cannot lock themselves out: %s %s", req.method, req.body)
	}
	assert.Equal(t, http.StatusOK, as(h, c, http.MethodPatch, "/api/users/root", `{"password":"a new long password"}`, true).Code)

	rec = as(h, c, http.MethodGet, "/api/users", "", false)
	require.Equal(t, http.StatusOK, rec.Code)
	var users []models.User
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &users))
	assert.Len(t, users, 2)

	assert.Equal(t, http.StatusNoContent, as(h, c, http.MethodDelete, "/api/users/ahmet", "", true).Code)
	assert.Equal(t, http.StatusNotFound, as(h, c, http.MethodDelete, "/api/users/ahmet", "", true).Code)
}

func TestTheAPITokenManagesUsers(t *testing.T) {
	m := newMemAccounts()
	access := NewAccess(AccessConfig{APIToken: writeToken, Accounts: m, Log: slog.New(slog.DiscardHandler)})
	h := NewRouter(Handlers{Agents: http.NotFoundHandler(), Access: access, DB: &mockPinger{}}, slog.New(slog.DiscardHandler))

	req := httptest.NewRequest(http.MethodPost, "/api/users", strings.NewReader(`{"username":"first","role":"admin","password":"`+password+`"}`))
	req.Header.Set("Authorization", "Bearer "+writeToken)
	rec := send(h, req)
	assert.Equal(t, http.StatusCreated, rec.Code, "the API token is an admin: it creates the first user")

	req = httptest.NewRequest(http.MethodPost, "/api/me/tokens", strings.NewReader(`{"name":"x"}`))
	req.Header.Set("Authorization", "Bearer "+writeToken)
	assert.Equal(t, http.StatusForbidden, send(h, req).Code, "the server's token has no tokens of its own")
}

func TestWritesNeedATokenOrUsers(t *testing.T) {
	m := newMemAccounts()
	h, _ := accountsRouter(t, m, true)
	rec := as(h, nil, http.MethodPost, "/api/jobs/1/cancel", "", false)
	assert.Equal(t, http.StatusForbidden, rec.Code, "nobody could ever be allowed")

	addUser(t, m, "ahmet", "maintainer")
	assert.Equal(t, http.StatusUnauthorized, as(h, nil, http.MethodPost, "/api/jobs/1/cancel", "", false).Code,
		"with users, a sign-in would do")
}

func TestBootstrapAdmin(t *testing.T) {
	m := newMemAccounts()
	created, err := BootstrapAdmin(t.Context(), m, "admin", password)
	require.NoError(t, err)
	assert.True(t, created)
	u, hash, err := m.User(t.Context(), "admin")
	require.NoError(t, err)
	assert.Equal(t, "admin", u.Role)
	assert.True(t, auth.CheckPassword(hash, password))

	created, err = BootstrapAdmin(t.Context(), m, "admin", "a different password")
	require.NoError(t, err)
	assert.False(t, created, "an existing admin keeps their password")
	_, hash, _ = m.User(t.Context(), "admin")
	assert.True(t, auth.CheckPassword(hash, password))
}
