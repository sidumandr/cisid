// Package web serves cisid's browser UI: a job list and a job page with
// live logs. It is a static page embedded in the binary that talks to the
// JSON API; it loads nothing from other origins.
package web

import (
	"embed"
	"io/fs"
	"net/http"
)

//go:embed static
var static embed.FS

// contentSecurityPolicy allows only this origin; the UI inserts API data
// as text, and this keeps injected markup from running even if it did not.
const contentSecurityPolicy = "default-src 'self'; img-src 'self' data:; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'none'"

// Handler serves the UI:
//
//	GET /             job list
//	GET /jobs/{id}    job page
//	GET /ui/...       scripts and styles
func Handler() http.Handler {
	files, err := fs.Sub(static, "static")
	if err != nil {
		panic(err) // the embedded directory always exists
	}
	index, err := fs.ReadFile(files, "index.html")
	if err != nil {
		panic(err)
	}

	page := func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}

	mux := http.NewServeMux()
	mux.HandleFunc("GET /{$}", page)
	mux.HandleFunc("GET /jobs/{id}", page)
	mux.Handle("GET /ui/", http.StripPrefix("/ui/", http.FileServerFS(files)))

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Security-Policy", contentSecurityPolicy)
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")
		mux.ServeHTTP(w, r)
	})
}
