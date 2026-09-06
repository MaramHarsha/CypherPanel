package rest

import (
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/updates"
)

// PanelInfo answers "what build is this, and is there a newer one"
// (consumer-defined; *updates.Checker satisfies it — control-plane-hardening.md
// §3). Latest is nil whenever there is nothing to say: up to date, the check
// is off, it has not completed, or this is not a release build.
type PanelInfo interface {
	Current() updates.Info
	Latest() *updates.Release
}

// PanelLogTail is the panel's own bounded log tail (consumer-defined;
// *logring.Ring satisfies it — §4).
type PanelLogTail interface {
	Tail(n int) []string
	Capacity() int
}

// panelLogsMaxTail bounds what one request may ask for. The ring holds no more
// than this anyway; the cap is here so a bad `tail` is a 400 the caller can
// see rather than a silent clamp.
const panelLogsMaxTail = 500

// panelLogsDefaultTail is what the report-issue dialog attaches when it asks
// for no particular number (canvas 13ai).
const panelLogsDefaultTail = 50

type panelVersionResponse struct {
	Version   string  `json:"version"`
	Commit    string  `json:"commit"`
	BuiltAt   *string `json:"built_at"`
	GoVersion string  `json:"go_version"`
	// No agent_min_version: no compatibility floor exists to declare yet, and
	// a documented field the server never sends is a lie in the contract
	// (spec §12 puts it out of scope; ENGINEERING rule 10).
	Latest *panelLatestResult `json:"latest"`
}

type panelLatestResult struct {
	Version     string  `json:"version"`
	Kind        string  `json:"kind"`
	NotesURL    string  `json:"notes_url"`
	PublishedAt *string `json:"published_at"`
}

type panelLogsResponse struct {
	Lines    []string `json:"lines"`
	Capacity int      `json:"capacity"`
}

// handleGetPanelVersion reports the running build and, when the update check
// has seen one, the newer release. Any authenticated principal may read it:
// the version is in every startup log line and the report-issue dialog needs
// it for every user, so hiding it from members would buy nothing (§9).
func (a *API) handleGetPanelVersion(w http.ResponseWriter, r *http.Request) {
	if a.deps.Panel == nil {
		writeError(w, http.StatusServiceUnavailable, "version information is not available on this panel")
		return
	}
	info := a.deps.Panel.Current()
	resp := panelVersionResponse{
		Version:   info.Version,
		Commit:    info.Commit,
		GoVersion: info.GoVersion,
		BuiltAt:   rfc3339OrNil(info.BuiltAt),
	}
	if latest := a.deps.Panel.Latest(); latest != nil {
		resp.Latest = &panelLatestResult{
			Version:     latest.Version,
			Kind:        latest.Kind,
			NotesURL:    latest.NotesURL,
			PublishedAt: rfc3339OrNil(latest.PublishedAt),
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

// handleGetPanelLogs hands a panel owner the tail of the panel's own log.
// Owner-only and session-only (the route is registered through sessionOnly):
// the log names hosts, resources and users, and an API token — which may live
// in CI — must never be able to lift it (§9). Secrets are not in it by
// construction (ENGINEERING rule 20), which
// TestPanelLogsNeverCarrySealedValues asserts rather than assumes.
func (a *API) handleGetPanelLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.PanelLogs == nil {
		writeError(w, http.StatusServiceUnavailable, "the panel log tail is not enabled on this panel")
		return
	}
	tail := panelLogsDefaultTail
	if raw := r.URL.Query().Get("tail"); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil || n < 1 || n > panelLogsMaxTail {
			writeError(w, http.StatusBadRequest, "tail must be a whole number between 1 and "+strconv.Itoa(panelLogsMaxTail))
			return
		}
		tail = n
	}
	lines := a.deps.PanelLogs.Tail(tail)
	if lines == nil {
		lines = []string{}
	}
	writeJSON(w, http.StatusOK, panelLogsResponse{Lines: lines, Capacity: a.deps.PanelLogs.Capacity()})
}

// rfc3339OrNil renders a timestamp, or nil when the build carried none — an
// omitted date is a fact about the build, not a zero instant in 1 AD.
func rfc3339OrNil(t time.Time) *string {
	if t.IsZero() {
		return nil
	}
	s := t.UTC().Format(time.RFC3339)
	return &s
}
