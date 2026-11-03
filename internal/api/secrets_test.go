package api

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/sidumandr/cisid/internal/secrets"
	"github.com/sidumandr/cisid/pkg/models"
)

// memVault is an in-memory SecretVault that applies the real validation.
type memVault struct {
	mu       sync.Mutex
	values   map[string]string   // "repo name" -> value
	branches map[string][]string // "repo name" -> branches
}

func (v *memVault) Set(_ context.Context, repo, name, value string, branches []string) error {
	if err := secrets.ValidateName(name); err != nil {
		return err
	}
	if err := secrets.ValidateBranches(branches); err != nil {
		return err
	}
	if len(value) > secrets.MaxValueBytes {
		return secrets.ErrTooLarge
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if v.values == nil {
		v.values, v.branches = map[string]string{}, map[string][]string{}
	}
	v.values[repo+" "+name] = value
	v.branches[repo+" "+name] = branches
	return nil
}

func (v *memVault) Delete(_ context.Context, repo, name string) error {
	v.mu.Lock()
	defer v.mu.Unlock()
	if _, ok := v.values[repo+" "+name]; !ok {
		return secrets.ErrNotFound
	}
	delete(v.values, repo+" "+name)
	return nil
}

func (v *memVault) List(_ context.Context, repo string) ([]models.SecretInfo, error) {
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []models.SecretInfo
	for k := range v.values {
		if r, name, _ := strings.Cut(k, " "); r == repo {
			out = append(out, models.SecretInfo{Name: name, UpdatedAt: time.Unix(0, 0).UTC(), Branches: v.branches[k]})
		}
	}
	return out, nil
}

func secretsRouter(vault SecretVault, token string) http.Handler {
	return NewRouter(Handlers{
		Webhook: http.NotFoundHandler(),
		Agents:  http.NotFoundHandler(),
		Secrets: NewSecretsAPI(vault, NewAccess(AccessConfig{APIToken: token, PublicRead: true}), slog.New(slog.DiscardHandler)),
		DB:      &mockPinger{},
	}, slog.New(slog.DiscardHandler))
}

func do(h http.Handler, method, target, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func TestSecretsAPI(t *testing.T) {
	vault := &memVault{}
	h := secretsRouter(vault, writeToken)
	const base = "/api/repos/sidumandr/cisid/secrets"

	rec := do(h, http.MethodPut, base+"/DEPLOY_TOKEN", writeToken, `{"value":"dep_123"}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())
	assert.Equal(t, "dep_123", vault.values["sidumandr/cisid DEPLOY_TOKEN"])

	rec = do(h, http.MethodGet, base, writeToken, "")
	require.Equal(t, http.StatusOK, rec.Code)
	assert.NotContains(t, rec.Body.String(), "dep_123", "values are never returned")
	var infos []models.SecretInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &infos))
	assert.Equal(t, "DEPLOY_TOKEN", infos[0].Name)

	assert.Equal(t, http.StatusNoContent, do(h, http.MethodDelete, base+"/DEPLOY_TOKEN", writeToken, "").Code)
	assert.Equal(t, http.StatusNotFound, do(h, http.MethodDelete, base+"/DEPLOY_TOKEN", writeToken, "").Code)
}

func TestSecretsAPIBranches(t *testing.T) {
	vault := &memVault{}
	h := secretsRouter(vault, writeToken)
	const base = "/api/repos/sidumandr/cisid/secrets"

	rec := do(h, http.MethodPut, base+"/DEPLOY_TOKEN", writeToken, `{"value":"dep_123","branches":["main","release/*"]}`)
	require.Equal(t, http.StatusNoContent, rec.Code, rec.Body.String())

	rec = do(h, http.MethodGet, base, writeToken, "")
	var infos []models.SecretInfo
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &infos))
	assert.Equal(t, []string{"main", "release/*"}, infos[0].Branches)

	for _, body := range []string{`{"value":"x","branches":[""]}`, `{"value":"x","branches":["a b"]}`} {
		assert.Equal(t, http.StatusBadRequest, do(h, http.MethodPut, base+"/TOKEN", writeToken, body).Code, body)
	}
}

func TestSecretsAPIRejectsBadInput(t *testing.T) {
	h := secretsRouter(&memVault{}, writeToken)
	const base = "/api/repos/sidumandr/cisid/secrets"

	tests := []struct {
		name, method, target, body string
		want                       int
	}{
		{"bad name", http.MethodPut, base + "/BAD-NAME", `{"value":"x"}`, http.StatusBadRequest},
		{"reserved name", http.MethodPut, base + "/CISID_JOB_ID", `{"value":"x"}`, http.StatusBadRequest},
		{"missing value", http.MethodPut, base + "/TOKEN", `{}`, http.StatusBadRequest},
		{"unknown field", http.MethodPut, base + "/TOKEN", `{"value":"x","note":"y"}`, http.StatusBadRequest},
		{"not json", http.MethodPut, base + "/TOKEN", `value=x`, http.StatusBadRequest},
		{"too large", http.MethodPut, base + "/TOKEN", `{"value":"` + strings.Repeat("x", secrets.MaxValueBytes+1) + `"}`, http.StatusRequestEntityTooLarge},
		{"bad repo", http.MethodGet, "/api/repos/sid%20x/cisid/secrets", "", http.StatusBadRequest},
		{"dot repo", http.MethodGet, "/api/repos/sidumandr/./secrets", "", http.StatusTemporaryRedirect}, // cleaned by ServeMux
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, do(h, tt.method, tt.target, writeToken, tt.body).Code)
		})
	}
}

func TestSecretsAPIAccess(t *testing.T) {
	const target = "/api/repos/sidumandr/cisid/secrets"

	assert.Equal(t, http.StatusUnauthorized, do(secretsRouter(&memVault{}, writeToken), http.MethodGet, target, "", "").Code)
	assert.Equal(t, http.StatusForbidden, do(secretsRouter(&memVault{}, ""), http.MethodGet, target, "x", "").Code,
		"no API token configured")

	rec := do(secretsRouter(nil, writeToken), http.MethodGet, target, writeToken, "")
	assert.Equal(t, http.StatusForbidden, rec.Code)
	assert.Contains(t, rec.Body.String(), "CISID_SECRET_KEY")
}
