// Package web is the phone page dev serve gives at /: static HTML, CSS and JS that read /v1/board and post to /v1.
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"path"
)

//go:embed static
var static embed.FS

// types fixes the page's own types, as a system's mime tables may name JS otherwise.
var types = map[string]string{".css": "text/css; charset=utf-8", ".js": "text/javascript; charset=utf-8"}

// csp lets the page load and call only its own Mac, and no other page frame it.
const csp = "default-src 'self'; frame-ancestors 'none'"

// Handler serves the page's files, index.html at /.
func Handler() http.Handler {
	sub, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	files := http.FileServerFS(sub)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if t, ok := types[path.Ext(r.URL.Path)]; ok {
			w.Header().Set("Content-Type", t)
		}
		w.Header().Set("Content-Security-Policy", csp)
		// A reload after an upgrade gets the new files
		w.Header().Set("Cache-Control", "no-cache")
		files.ServeHTTP(w, r)
	})
}
