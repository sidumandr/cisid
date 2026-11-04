package api

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/notify"
	"github.com/sidumandr/cisid/pkg/models"
)

// memNotifications is an in-memory NotificationService.
type memNotifications struct {
	mu      sync.Mutex
	list    map[string]models.Notification // "repo name"
	targets map[string]notify.Target
	tested  []string
	sendErr error
}

func (m *memNotifications) Put(_ context.Context, n models.Notification, t notify.Target) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if n.Kind != models.NotifySlack && n.Kind != models.NotifyWebhook {
		return fmt.Errorf("%w: kind", notify.ErrInvalid)
	}
	if m.list == nil {
		m.list, m.targets = map[string]models.Notification{}, map[string]notify.Target{}
	}
	n.Target = "hooks.slack.com"
	m.list[n.Repo+" "+n.Name] = n
	m.targets[n.Repo+" "+n.Name] = t
	return nil
}

func (m *memNotifications) Delete(_ context.Context, repo, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.list[repo+" "+name]; !ok {
		return notify.ErrNotFound
	}
	delete(m.list, repo+" "+name)
	return nil
}

func (m *memNotifications) List(_ context.Context, repo string) ([]models.Notification, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var out []models.Notification
	for _, n := range m.list {
		if n.Repo == repo {
			out = append(out, n)
		}
	}
	return out, nil
}

func (m *memNotifications) Test(_ context.Context, repo, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.list[repo+" "+name]; !ok {
		return notify.ErrNotFound
	}
	m.tested = append(m.tested, repo+" "+name)
	return m.sendErr
}

func notificationsRouter(service NotificationService, access *Access) http.Handler {
	if access == nil {
		access = tokenAccess("")
	}
	return NewRouter(Handlers{
		Agents:        http.NotFoundHandler(),
		Notifications: NewNotificationsAPI(service, access, slog.New(slog.DiscardHandler)),
		Access:        access,
		DB:            &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func TestNotificationsAPI(t *testing.T) {
	service := &memNotifications{}
	h := notificationsRouter(service, nil)
	const base = "/api/repos/gitea:office/app/notifications"

	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodGet, base, "", "").Code, "maintainers only, reading too")
	assert.Equal(t, http.StatusUnauthorized, call(h, http.MethodPut, base+"/chat", "", `{"kind":"slack"}`).Code)

	rec := call(h, http.MethodPut, base+"/chat", writeToken,
		`{"kind":"slack","url":"https://hooks.slack.com/services/T/B/X","on":["broken","fixed"],"branches":["main"]}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, notify.Target{URL: "https://hooks.slack.com/services/T/B/X"}, service.targets["gitea:office/app chat"])
	saved := service.list["gitea:office/app chat"]
	assert.Equal(t, []string{"broken", "fixed"}, saved.On)
	assert.Equal(t, []string{"main"}, saved.Branches)

	rec = call(h, http.MethodGet, base, writeToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "/services/", "the URL is never read back")
	var list []models.Notification
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &list))
	require.Len(t, list, 1)
	assert.Equal(t, "hooks.slack.com", list[0].Target)

	empty := call(h, http.MethodGet, "/api/repos/other/repo/notifications", writeToken, "")
	assert.JSONEq(t, `[]`, empty.Body.String())

	for _, body := range []string{`{"kind":"teams","url":"https://x"}`, `{"kind":"slack","color":"red"}`, `nope`} {
		rec := call(h, http.MethodPut, base+"/chat", writeToken, body)
		assert.Equal(t, http.StatusBadRequest, rec.Code, body)
	}

	assert.Equal(t, http.StatusNoContent, call(h, http.MethodPost, base+"/chat/test", writeToken, "").Code)
	assert.Equal(t, []string{"gitea:office/app chat"}, service.tested)
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodPost, base+"/missing/test", writeToken, "").Code)
	service.sendErr = fmt.Errorf("%w: hooks.slack.com answered 404 Not Found", notify.ErrSend)
	rec = call(h, http.MethodPost, base+"/chat/test", writeToken, "")
	assert.Equal(t, http.StatusBadGateway, rec.Code)
	assert.Contains(t, rec.Body.String(), "answered 404 Not Found")

	assert.Equal(t, http.StatusNoContent, call(h, http.MethodDelete, base+"/chat", writeToken, "").Code)
	assert.Equal(t, http.StatusNotFound, call(h, http.MethodDelete, base+"/chat", writeToken, "").Code)
	assert.Equal(t, http.StatusBadRequest, call(h, http.MethodGet, "/api/repos/svn:a/b/notifications", writeToken, "").Code)
}

func TestNotificationsAPIDisabled(t *testing.T) {
	h := notificationsRouter(nil, nil)
	rec := call(h, http.MethodGet, "/api/repos/sidumandr/cisid/notifications", writeToken, "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "CISID_SECRET_KEY")
}
