package rest

// The in-panel changelog (panel-updates.md §9).
//
// It surfaces as one quiet dot on the help menu after an update — never a modal
// takeover. The content is embedded in the binary; the AVAILABLE release
// contributes only its version, its kind and a link pointing out, so no prose
// from a network source is ever rendered here.

import (
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/changelog"
)

type changelogResponse struct {
	Current string            `json:"current"`
	Entries []changelog.Entry `json:"entries"`
	// Available is the newer release, when there is one. It carries no notes:
	// this binary predates it and cannot have its changelog, and fetching one
	// would put untrusted prose on an operator-facing page.
	Available *availableRelease `json:"available"`
}

type availableRelease struct {
	Version  string `json:"version"`
	Kind     string `json:"kind"`
	NotesURL string `json:"notes_url"`
}

func (a *API) handleChangelog(w http.ResponseWriter, r *http.Request) {
	out := changelogResponse{Entries: changelog.Entries()}
	if a.deps.Updates != nil {
		out.Current = a.deps.Updates.Current().Version
		if latest := a.deps.Updates.Latest(); latest != nil {
			out.Available = &availableRelease{
				Version: latest.Version, Kind: latest.Kind, NotesURL: latest.NotesURL,
			}
		}
	}
	writeJSON(w, http.StatusOK, out)
}
