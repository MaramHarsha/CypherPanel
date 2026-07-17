package rest

import (
	"embed"
	"net/http"
)

// consoleFS holds the interim status console — a single self-contained HTML
// page embedded into the binary (ADR-001: the UI ships inside cypherd). This is
// the Phase 1 bootstrap console, not the React app in web/ (which arrives with
// its own toolchain); it exists so Phase 1 has a working "servers visible with
// live status" page as the roadmap requires.
//
//go:embed console/index.html
var consoleFS embed.FS

func (a *API) consoleHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			writeError(w, http.StatusNotFound, "not found")
			return
		}
		data, err := consoleFS.ReadFile("console/index.html")
		if err != nil {
			a.deps.Log.Error("reading console asset", "error", err)
			writeError(w, http.StatusInternalServerError, "console unavailable")
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
}
