package metrics

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func scrape(t *testing.T) string {
	t.Helper()
	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	return rec.Body.String()
}

func TestRegisteredCollectors(t *testing.T) {
	RegisterAgents(func() int { return 3 })
	RegisterJobCounts(func(context.Context) (map[string]int, error) {
		return map[string]int{"pending": 2, "running": 1}, nil
	}, slog.New(slog.DiscardHandler))

	out := scrape(t)
	assert.Contains(t, out, "cisid_agents_connected 3")
	assert.Contains(t, out, `cisid_jobs{status="pending"} 2`)
	assert.Contains(t, out, `cisid_jobs{status="running"} 1`)

	// A restarted server registers again without a duplicate-registration panic.
	RegisterAgents(func() int { return 5 })
	assert.Contains(t, scrape(t), "cisid_agents_connected 5")
}

func TestJobCountsErrorDoesNotBreakTheScrape(t *testing.T) {
	RegisterJobCounts(func(context.Context) (map[string]int, error) {
		return nil, errors.New("db down")
	}, slog.New(slog.DiscardHandler))

	rec := httptest.NewRecorder()
	Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))

	assert.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), "go_goroutines", "the other metrics are still served")
	assert.NotContains(t, rec.Body.String(), "cisid_jobs{")
}
