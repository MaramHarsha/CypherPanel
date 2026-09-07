package rest

// Project export (project-export.md). Leaving is deliberately easy.

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/export"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// handleExportProject streams the project as a gzipped tar.
//
// Team ADMIN, not member: the archive is the whole configuration of a project
// in one file, and while it carries no secret VALUES (spec §4), an env-var key
// list and a domain map are still the shape of someone's infrastructure. That
// is the rank registries and deploy protection use for the same reason.
//
// Everything that can fail happens before the first byte: once the writer has
// started, the status code is spent, and a truncated gzip fails its own CRC
// rather than opening as a smaller, plausible-looking, wrong archive (§5).
func (a *API) handleExportProject(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	id := r.PathValue("id")
	if !a.requireProjectRole(w, r, user, id, domain.RoleAdmin) {
		return
	}
	if a.deps.Export == nil {
		writeError(w, http.StatusNotImplemented, "export is not enabled on this panel")
		return
	}
	proj, err := a.deps.Projects.Get(r.Context(), id)
	if errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "project not found")
		return
	}
	if err != nil {
		a.deps.Log.Error("getting project for export", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the project")
		return
	}

	// Audited before the stream starts, because an export is a bulk read of a
	// project's configuration and the record must exist even if the download
	// is abandoned halfway.
	a.audit(r, audit.Entry{
		Action:   audit.ActionProjectExported,
		Resource: audit.Resource(audit.ResourceProject, proj.ID, proj.Name),
		TeamID:   proj.TeamID,
	})

	name := export.Filename(proj.Slug, time.Now().UTC().Format("2006-01-02"))
	w.Header().Set("Content-Type", "application/gzip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", name))
	// No Content-Length: the archive is streamed, never buffered, so its size
	// is not known until it is finished.
	if err := a.deps.Export.WriteTo(r.Context(), w, id); err != nil {
		// The header is already written, so there is no status left to send.
		// Log it and let the truncated gzip refuse itself on the far side.
		a.deps.Log.Error("streaming project export", "project_id", id, "error", err)
	}
}
