package rest

// "Use this machine" — enroll the panel's own host as a server with no command
// to paste (local-server.md §7).
//
// The POST is OWNER and SESSION-ONLY, and the reason is the same one break
// glass and the agent channel already carry: this installs software on the
// panel's own host as root. An API token that can do that is an API token that
// owns the box, and API tokens live in CI. The GET is admin, because knowing
// whether the button is available grants nothing.
//
// `POST /api/v1/servers` stays panel-admin and token-reachable: handing out a
// join command grants nothing by itself, and a provisioning script that enrols
// servers is the reason that route is reachable by a token at all.

import (
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/localjoin"
	"github.com/MaramHarsha/cypherpanel/core/updates"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

type localServerDTO struct {
	// State is one of available | helper_missing | unsupported | already_joined
	// — NAMED rather than discovered by a failure (local-server.md §5).
	State string `json:"state"`
	// Reason is the sentence the dialog shows in place of the button when the
	// state is not `available`. A disabled control with no explanation is the
	// dead end ui-principles §11 is named against.
	Reason string `json:"reason,omitempty"`
	// Hostname is this host, so the button can name the machine it will change.
	Hostname string `json:"hostname"`
	// ServerID is set when this host is already enrolled, so the dialog can
	// offer a link instead of a button.
	ServerID string `json:"server_id,omitempty"`
	// Phase mirrors the helper's own progress while an install is running.
	Phase  string `json:"phase,omitempty"`
	Detail string `json:"detail,omitempty"`
}

// localState resolves what the panel can honestly offer right now.
func (a *API) localState() localServerDTO {
	out := localServerDTO{Hostname: a.deps.PublicHost}
	id, joined, err := localjoin.LocalServerID(localjoin.AgentIdentityPath)
	switch {
	case joined:
		out.State = localjoin.StateAlreadyJoined
		out.ServerID = id
		out.Reason = "this machine is already running an agent for this panel"
		return out
	case err != nil:
		// FAIL CLOSED. The panel runs under DynamicUser and the agent's state
		// directory belongs to root; if that read is refused, the honest answer
		// is "cannot tell", never "not joined". Guessing the negative here is
		// what created a second Server row and a second join token every time
		// somebody clicked the button on an already-enrolled machine.
		a.deps.Log.Warn("reading the local agent identity", "path", localjoin.AgentIdentityPath, "error", err)
		out.State = localjoin.StateUnknown
		out.Reason = "the panel cannot read this machine's agent identity, so it cannot tell whether an agent is already running here — upgrade the agent on this host, or add the server with its join command"
		return out
	}
	dir := localjoin.Dir(a.deps.UpgradeDir)
	switch {
	case a.deps.UpgradeDir == "":
		out.State = localjoin.StateUnsupported
		out.Reason = "this panel runs in a container, so there is no host service manager to install an agent into — run the join command on the machine you want to add"
	case !dir.Writable():
		// Every panel installed before this feature is here, so it has to read
		// as a version gap rather than as a bug.
		out.State = localjoin.StateHelperMissing
		out.Reason = "this panel was installed before the local-agent helper existed — re-run install.sh on this host to add it"
	case a.deps.LocalPortInUse != nil && (a.deps.LocalPortInUse("80") || a.deps.LocalPortInUse("443")):
		// The agent's Proxy needs 80 and 443 on this host, and something else
		// already answers there — usually a reverse proxy in front of the
		// panel. Joining anyway produces a server whose Proxy cannot bind and
		// which every deploy on it then fails, reported as degraded but never
		// as this sentence.
		out.State = localjoin.StateUnsupported
		out.Reason = "ports 80 and 443 on this host are already in use, and the agent's Proxy needs both — add a different server, or free them first"
	default:
		out.State = localjoin.StateAvailable
	}
	if s, ok, err := dir.ReadStatus(); err == nil && ok {
		out.Phase, out.Detail = s.Phase, s.Detail
		if s.ServerID != "" && out.ServerID == "" {
			out.ServerID = s.ServerID
		}
	}
	return out
}

func (a *API) handleGetLocalServer(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleAdmin) {
		return
	}
	writeJSON(w, http.StatusOK, a.localState())
}

// handleCreateLocalServer creates the Server through the ORDINARY path and then
// writes a request the root helper performs. The plane installs nothing itself
// — see local-server.md §2 for why that is a design constraint rather than an
// implementation detail.
func (a *API) handleCreateLocalServer(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if !a.requirePanelRole(w, user, domain.RoleOwner) {
		return
	}
	state := a.localState()
	switch state.State {
	case localjoin.StateAvailable:
	case localjoin.StateAlreadyJoined:
		writeError(w, http.StatusConflict, state.Reason)
		return
	default:
		// 501, not 400: the request is well formed and this panel simply cannot
		// perform it. The reason names the remedy.
		writeError(w, http.StatusNotImplemented, state.Reason)
		return
	}

	name := a.deps.PublicHost
	if name == "" {
		name = "this machine"
	}
	srv, token, err := a.deps.Servers.Create(r.Context(), name)
	if err != nil {
		a.deps.Log.Error("creating the local server", "error", err)
		writeError(w, http.StatusInternalServerError, "could not create the server")
		return
	}
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerCreated,
		Resource: audit.Resource(audit.ResourceServer, srv.ID, srv.Name),
	})

	// Set the public address, which for THIS server the panel actually knows.
	//
	// ADR-002 is why a joined server's address is operator-supplied: the agent
	// dials out and the heartbeat carries no address, so the plane cannot learn
	// where a remote host is reachable. None of that applies to the machine the
	// plane is running on — CYPHERD_PUBLIC_HOST is that address, it is already
	// in the certificate SANs and the join command, and leaving it blank here
	// would silently disable DNS automation for every application placed on this
	// server (dns-automation.md §6: with no public address there is nothing to
	// point a record at, so records are never created). A blank field nobody
	// knows to fill in is the whole failure.
	if a.deps.ServerAddresses != nil && a.deps.PublicHost != "" {
		if updated, aerr := a.deps.ServerAddresses.SetServerPublicAddress(r.Context(), srv.ID, a.deps.PublicHost); aerr != nil {
			// Not fatal: the server exists and works, and the address is one
			// field on its own page. DNS automation is what is degraded, and
			// GET /applications/{id}/dns already names that as its reason.
			a.deps.Log.Warn("setting the local server's public address", "server_id", srv.ID, "error", aerr)
		} else {
			srv = updated
		}
	}

	sum := sha256.Sum256(a.deps.CACertPEM)
	agentURL := ""
	if a.deps.Panel != nil {
		agentURL = updates.AgentAssetURL(a.deps.Panel.Current().Version)
	}
	req := localjoin.NewRequest(localjoin.Request{
		ID:            ids.New("ljn"),
		Token:         token,
		EnrollAddr:    a.deps.EnrollAddr,
		PlaneHTTP:     a.deps.ConsoleURL,
		CAFingerprint: hex.EncodeToString(sum[:]),
		AgentURL:      agentURL,
		ServerID:      srv.ID,
		Actor:         user.Email,
	}, time.Now().UTC(), ids.New("non"))

	if err := localjoin.Dir(a.deps.UpgradeDir).WriteRequest(req); err != nil {
		// The Server row is left in place deliberately: it is exactly the state
		// a paste-based join produces before the command is run, and the join
		// command on its detail page is a working way to finish.
		a.deps.Log.Error("placing the local join request", "server_id", srv.ID, "error", err)
		writeError(w, http.StatusInternalServerError,
			"the server was created but the install request could not be placed — use its join command instead")
		return
	}
	// A separate action from server.created: "an admin generated a join
	// command" and "an owner installed an agent on the control plane" are
	// different acts with different blast radii, and an audit log that cannot
	// tell them apart is not much of one.
	a.audit(r, audit.Entry{
		Action:   audit.ActionServerLocalJoin,
		Resource: audit.Resource(audit.ResourceServer, srv.ID, srv.Name),
		Detail:   map[string]any{"request_id": req.ID},
	})

	writeJSON(w, http.StatusAccepted, struct {
		Server serverDTO      `json:"server"`
		Local  localServerDTO `json:"local"`
	}{
		Server: toServerDTO(srv),
		Local: localServerDTO{
			State: localjoin.StateAvailable, Hostname: state.Hostname,
			ServerID: srv.ID, Phase: localjoin.PhaseInstalling,
			Detail: "installing the agent on this host",
		},
	})
}
