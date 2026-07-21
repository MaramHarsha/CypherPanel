// Package webui embeds the built web application (web/dist, ADR-001: the UI
// ships inside cypherd — no SSR server) and serves it with a strict CSP and
// the SPA fallback client routes need (web-ui-design.md §5).
//
// dist/ is produced by `make build-web` and committed, so `go build` never
// needs a Node toolchain.
package webui

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

// csp locks the app to its own origin: no external scripts, styles, fonts, or
// connections — the property that keeps a bearer token in localStorage
// defensible (web-ui-design.md §5). style-src allows inline styles because
// positioning libraries set style attributes; script-src stays strict.
const csp = "default-src 'self'; script-src 'self'; style-src 'self' 'unsafe-inline'; " +
	"img-src 'self' data:; font-src 'self'; connect-src 'self'; " +
	"frame-ancestors 'none'; base-uri 'none'; form-action 'self'"

// Handler serves the embedded app: real files as-is (hashed assets cached
// immutably), everything else falls back to index.html so client-side routes
// deep-link. The caller keeps /api/* away from this handler.
func Handler() (http.Handler, error) {
	sub, err := fs.Sub(distFS, "dist")
	if err != nil {
		return nil, err
	}
	index, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		return nil, err
	}
	files := http.FileServerFS(sub)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(r.URL.Path, "/")
		if path != "" && path != "index.html" {
			if info, statErr := fs.Stat(sub, path); statErr == nil && !info.IsDir() {
				if strings.HasPrefix(path, "assets/") {
					// Vite content-hashes everything under assets/.
					w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
				}
				files.ServeHTTP(w, r)
				return
			}
		}
		w.Header().Set("Content-Security-Policy", csp)
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Header().Set("Cache-Control", "no-cache")
		_, _ = w.Write(index)
	}), nil
}
