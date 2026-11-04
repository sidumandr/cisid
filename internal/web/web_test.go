package web

import (
	"io/fs"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func get(h http.Handler, path string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
	return rec
}

// scriptRe finds the built entry script the page loads.
var scriptRe = regexp.MustCompile(`<script type="module" crossorigin src="(/ui/assets/[^"]+\.js)"`)

func TestPagesServeTheApp(t *testing.T) {
	h := Handler()
	for _, path := range []string{"/", "/login", "/register", "/jobs", "/jobs/42", "/repos", "/repos/sidumandr/cisid",
		"/repos/gitlab%3Agroup%2Fsub/project", "/account", "/users"} {
		rec := get(h, path)
		assert.Equal(t, http.StatusOK, rec.Code, path)
		assert.Equal(t, "text/html; charset=utf-8", rec.Header().Get("Content-Type"))
		assert.Regexp(t, scriptRe, rec.Body.String(), path)
	}
	assert.Equal(t, http.StatusNotFound, get(h, "/nothing-here").Code)
}

func TestAssets(t *testing.T) {
	h := Handler()
	m := scriptRe.FindStringSubmatch(get(h, "/").Body.String())
	require.Len(t, m, 2, "the page loads the built script")

	js := get(h, m[1])
	assert.Equal(t, http.StatusOK, js.Code)
	assert.Contains(t, js.Header().Get("Content-Type"), "javascript")

	cssRe := regexp.MustCompile(`href="(/ui/assets/[^"]+\.css)"`)
	c := cssRe.FindStringSubmatch(get(h, "/").Body.String())
	require.Len(t, c, 2)
	css := get(h, c[1])
	assert.Equal(t, http.StatusOK, css.Code)
	assert.Contains(t, css.Header().Get("Content-Type"), "text/css")
	assert.Contains(t, css.Body.String(), "url(/ui/fonts/authentic-sans-90.woff2)", "fonts resolve wherever the stylesheet is")

	font := get(h, "/ui/fonts/authentic-sans-90.woff2")
	assert.Equal(t, http.StatusOK, font.Code, "fonts are served from this origin, as the CSP requires")
	assert.Equal(t, "font/woff2", font.Header().Get("Content-Type"))

	for path, typ := range map[string]string{"/ui/favicon.svg": "image/svg+xml", "/ui/favicon.ico": "image/", "/ui/apple-touch-icon.png": "image/png"} {
		icon := get(h, path)
		assert.Equal(t, http.StatusOK, icon.Code, path)
		assert.Contains(t, icon.Header().Get("Content-Type"), typ, path)
	}

	assert.Equal(t, http.StatusNotFound, get(h, "/ui/missing.js").Code)
}

// TestNoInlineCode keeps the page loadable under the CSP, which allows no
// inline scripts or styles.
func TestNoInlineCode(t *testing.T) {
	page := get(Handler(), "/").Body.String()
	assert.NotRegexp(t, `<script(?:\s[^>]*)?>[^<]`, page, "no inline script")
	assert.NotContains(t, page, "<style")
	assert.NotContains(t, page, " style=")
}

// TestNoLongDashes keeps the em dash out of everything the UI serves, by
// the design's rule.
func TestNoLongDashes(t *testing.T) {
	err := fs.WalkDir(static, "static", func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || strings.HasSuffix(path, ".woff2") || strings.HasSuffix(path, ".ico") || strings.HasSuffix(path, ".png") {
			return err
		}
		data, err := static.ReadFile(path)
		if err != nil {
			return err
		}
		assert.NotContains(t, string(data), "—", path)
		return nil
	})
	require.NoError(t, err)
}

// TestSourceInsertsTextOnly checks the UI's source never hands API data to
// the browser as HTML: React renders it as text, and nothing bypasses that.
func TestSourceInsertsTextOnly(t *testing.T) {
	src := filepath.Join("..", "..", "web", "src")
	if _, err := os.Stat(src); err != nil {
		t.Skip("the web UI's source is not here")
	}
	banned := []string{"dangerouslySetInnerHTML", "innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"}
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, b := range banned {
			assert.NotContains(t, string(data), b, path)
		}
		return nil
	})
	require.NoError(t, err)
}

func TestSecurityHeaders(t *testing.T) {
	rec := get(Handler(), "/")
	csp := rec.Header().Get("Content-Security-Policy")
	assert.True(t, strings.HasPrefix(csp, "default-src 'self'"), csp)
	assert.NotContains(t, csp, "unsafe-inline")
	assert.Contains(t, csp, "frame-ancestors 'none'")
	assert.Equal(t, "nosniff", rec.Header().Get("X-Content-Type-Options"))
}
