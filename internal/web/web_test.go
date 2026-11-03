package web

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

func TestPagesServeTheApp(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/jobs/42", "/repos", "/repos/sidumandr/cisid"} {
		rec := get(h, path)
		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Contains(t, rec.Body.String(), `<script src="/ui/app.js"`)
	}
}

func TestAssets(t *testing.T) {
	h := Handler()

	js := get(h, "/ui/app.js")
	assert.Equal(t, http.StatusOK, js.Code)
	assert.Contains(t, js.Header().Get("Content-Type"), "javascript")
	assert.NotContains(t, js.Body.String(), "innerHTML", "API data is inserted as text only")

	css := get(h, "/ui/app.css")
	assert.Equal(t, http.StatusOK, css.Code)
	assert.Contains(t, css.Header().Get("Content-Type"), "text/css")

	font := get(h, "/ui/fonts/authentic-sans-90.woff2")
	assert.Equal(t, http.StatusOK, font.Code, "fonts are served from this origin, as the CSP requires")
	assert.Equal(t, "font/woff2", font.Header().Get("Content-Type"))

	assert.Equal(t, http.StatusNotFound, get(h, "/ui/missing.js").Code)
}

// TestNoLongDashes keeps the em dash out of the UI's words, by the
// design's rule.
func TestNoLongDashes(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/ui/app.js", "/ui/app.css"} {
		assert.NotContains(t, get(h, path).Body.String(), "—", path)
	}
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(Handler(), "/")
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, "default-src 'self'"), csp)
	assert.NotContains(t, csp, "unsafe-inline")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}
