package rest

// Agent version channels (agent-updates.md §7, ADR-010).
//
// The three mutating routes are OWNER and SESSION-ONLY. This is the one control
// in the panel that changes what code runs on every server, and an API token
// that can move a channel is an API token that owns the fleet — API tokens live
// in CI. It is the rule deploy protection set for break glass, applied where it
// matters most.
//
// The per-server channel move is its own route rather than a field on
// PATCH /servers/{id}. That PATCH is panel-admin and deliberately reachable by
// a provisioning token, so re-ranking the whole route would take public_address
// away from every admin that sets it today — while a field-level rank check
// would be the first in this API, and a new authorization precedent is a bad
// thing to introduce incidentally inside a feature: every check here is
// route-shaped, and a reviewer can see a route's rank at the mux.

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/agentupdates"
	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type agentChannelDTO struct {
	Channel        string `json:"channel"`
	DesiredVersion string `json:"desired_version"`
	ArtifactBase   string `json:"artifact_base"`
	// ResolvedArtifactBase is what an empty artifact_base actually resolves to,
	// so the screen shows where the binaries come from rather than a blank
	// field the operator has to know the default for.
	ResolvedArtifactBase string  `json:"resolved_artifact_base"`
	Rollback             bool    `json:"rollback"`
	UpdatedAt            string  `json:"updated_at"`
	UpdatedBy            *string `json:"updated_by"`
}

type agentServerDTO struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Status  string `json:"status"`
	Channel string `json:"channel"`
	Running string `json:"running_version"`
	Desired string `json:"desired_version"`
	// Converged is computed by the plane rather than by the screen, so "is it
	// done" has one answer and not two.
	Converged bool   `json:"converged"`
	Phase     string `json:"phase"`
	Target    string `json:"target_version"`
	Detail    string `json:"detail"`
}

type agentUpdatesDTO struct {
	PanelVersion string            `json:"panel_version"`
	Channels     []agentChannelDTO `json:"channels"`
	Servers      []agentServerDTO  `json:"servers"`
	Histogram    map[string]int    `json:"running_versions"`
}

type setAgentChannelRequest struct {
	// Version empty clears the instruction, which is how a rollout an operator
	// no longer wants is stopped.
	Version      string `json:"version"`
	ArtifactBase string `json:"artifact_base"`
}

type setServerChannelRequest struct {
	Channel string `json:"channel"`
}

func toAgentChannelDTO(c domain.AgentChannelRow) agentChannelDTO {
	resolved := c.ArtifactBase
	if resolved == "" && c.DesiredVersion != "" {
		resolved = agentupdates.DefaultArtifactBase(c.DesiredVersion)
	}
	return agentChannelDTO{
		Channel:              c.Channel,
		DesiredVersion:       c.DesiredVersion,
		ArtifactBase:         c.ArtifactBase,
		ResolvedArtifactBase: resolved,
		Rollback:             c.Rollback,
		UpdatedAt:            c.UpdatedAt.UTC().Format(time.RFC3339),
		UpdatedBy:            c.UpdatedBy,
	}
}

func (a *API) handleGetAgentUpdates(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleMember) {
		return
	}
	if a.deps.AgentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "agent updates are not enabled on this panel")
		return
	}
	view, err := a.deps.AgentUpdates.Get(r.Context())
	if err != nil {
		a.deps.Log.Error("reading agent update channels", "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the release channels")
		return
	}
	out := agentUpdatesDTO{PanelVersion: view.PanelVersion, Histogram: view.Histogram}
	for _, c := range view.Channels {
		out.Channels = append(out.Channels, toAgentChannelDTO(c))
	}
	for _, s := range view.Servers {
		channel := s.Server.AgentChannel
		if channel == "" {
			channel = domain.ChannelStable
		}
		out.Servers = append(out.Servers, agentServerDTO{
			ID: s.Server.ID, Name: s.Server.Name, Status: string(s.Server.Status),
			Channel: channel, Running: s.Server.AgentVersion, Desired: s.Desired,
			Converged: s.Converged, Phase: s.Server.AgentUpdatePhase,
			Target: s.Server.AgentUpdateTarget, Detail: s.Server.AgentUpdateDetail,
		})
	}
	if out.Servers == nil {
		out.Servers = []agentServerDTO{}
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleSetAgentChannel(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.AgentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "agent updates are not enabled on this panel")
		return
	}
	channel := r.PathValue("channel")
	var req setAgentChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	entry := audit.Entry{
		Action:   audit.ActionAgentVersionSet,
		Resource: audit.Resource(audit.ResourcePanel, "agent-channel:"+channel, channel),
		Detail:   map[string]any{"channel": channel, "version": strings.TrimSpace(req.Version)},
	}
	row, err := a.deps.AgentUpdates.Set(r.Context(), channel, req.Version, req.ArtifactBase, user.Email)
	switch {
	case err == nil:
	case errors.Is(err, agentupdates.ErrUnknownChannel):
		writeError(w, http.StatusNotFound, "no such release channel")
		return
	case errors.Is(err, agentupdates.ErrNewerThanPanel),
		errors.Is(err, agentupdates.ErrArtifactUnreachable),
		errors.Is(err, agentupdates.ErrBadVersion),
		errors.Is(err, agentupdates.ErrBadArtifactBase):
		a.auditFailed(r, entry, err.Error())
		writeError(w, http.StatusBadRequest, err.Error())
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "no such release channel")
		return
	default:
		a.deps.Log.Error("setting an agent channel", "channel", channel, "error", err)
		writeError(w, http.StatusInternalServerError, "could not set the desired agent version")
		return
	}
	a.audit(r, entry)
	writeJSON(w, http.StatusOK, toAgentChannelDTO(row))
}

// handlePromoteAgentChannel makes stable's desired version canary's. The gate's
// two refusals are 409 and NAME what they refuse on, because an operator who
// learns the gate refuses for reasons it will not explain is an operator who
// stops reading refusals.
func (a *API) handlePromoteAgentChannel(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.AgentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "agent updates are not enabled on this panel")
		return
	}
	row, err := a.deps.AgentUpdates.Promote(r.Context(), user.Email)
	switch {
	case err == nil:
	case errors.Is(err, agentupdates.ErrGateNoConverged), errors.Is(err, agentupdates.ErrGateRolledBack):
		writeError(w, http.StatusConflict, err.Error())
		return
	default:
		a.deps.Log.Error("promoting canary", "error", err)
		writeError(w, http.StatusInternalServerError, "could not promote canary")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionAgentPromoted,
		Resource: audit.Resource(audit.ResourcePanel, "agent-channel:stable", "stable"),
		Detail:   map[string]any{"version": row.DesiredVersion},
	})
	writeJSON(w, http.StatusOK, toAgentChannelDTO(row))
}

func (a *API) handleSetServerAgentChannel(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	if a.deps.AgentUpdates == nil {
		writeError(w, http.StatusServiceUnavailable, "agent updates are not enabled on this panel")
		return
	}
	id := r.PathValue("id")
	var req setServerChannelRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid JSON body")
		return
	}
	srv, err := a.deps.AgentUpdates.SetServerChannel(r.Context(), id, req.Channel)
	switch {
	case err == nil:
	case errors.Is(err, agentupdates.ErrUnknownChannel):
		writeError(w, http.StatusBadRequest, "channel must be stable or canary")
		return
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "server not found")
		return
	default:
		a.deps.Log.Error("setting a server's agent channel", "server_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not change the release channel")
		return
	}
	// The per-server move rides the existing server.updated rather than
	// minting a third action: it IS an update to a server, and a vocabulary
	// that grows a verb per field is a vocabulary nobody can query.
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerUpdated,
		Resource: audit.Resource(audit.ResourceServer, srv.ID, srv.Name),
		Detail:   map[string]any{"agent_channel": srv.AgentChannel},
	})
	writeJSON(w, http.StatusOK, toServerDTO(srv))
}
