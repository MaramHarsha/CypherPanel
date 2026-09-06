package rest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// invalidateEvent is the SSE payload: "this resource changed — refetch it". The
// stream deliberately carries no status value (web-ui-design.md §5): the client
// refetches via the API, so the stream never duplicates or drifts from the
// authoritative state.
type invalidateEvent struct {
	Resource string `json:"resource"` // application | database
	ID       string `json:"id"`
}

// handleEvents streams resource-change notifications as Server-Sent Events. It
// resolves each status observation's resource from its subject
// (state.<server>.app.<id> / state.<server>.db.<id>), filters to what the caller
// may see (a panel owner sees all; others only their teams' resources — same
// 404-invisible boundary as every other route), and emits an "invalidate"
// event. The client fetches current state on connect, then refetches named
// resources as they change (ui-principles §10).
func (a *API) handleEvents(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Logs == nil {
		writeError(w, http.StatusServiceUnavailable, "event stream unavailable")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}

	ctx := r.Context()
	// visible caches the per-connection resource→visibility decision so a busy
	// resource costs at most one authorization lookup per stream.
	vis := &visibilityCache{api: a, user: user, seen: map[string]bool{}}

	events := make(chan invalidateEvent, 256)
	stop, err := a.deps.Logs.SubscribeStatus(ctx, func(subject string, _ []byte) {
		ev, ok := resourceFromSubject(subject)
		if !ok || !vis.visible(ctx, ev) {
			return
		}
		select {
		case events <- ev:
		case <-ctx.Done():
		}
	})
	if err != nil {
		a.deps.Log.Error("subscribing to status", "user_id", user.ID, "trace_id", traceIDFromContext(ctx), "error", err)
		writeError(w, http.StatusInternalServerError, "could not subscribe to events")
		return
	}
	defer stop()

	// The opening frame carries the request's correlation id, as every other
	// response does (control-plane-hardening.md §2).
	if _, err := fmt.Fprintf(w, "event: connected\ndata: {\"trace_id\":%q}\n\n", traceIDFromContext(ctx)); err != nil {
		return
	}
	flusher.Flush()

	for {
		select {
		case <-ctx.Done():
			return
		case ev := <-events:
			data, _ := json.Marshal(ev)
			if _, err := fmt.Fprintf(w, "event: invalidate\ndata: %s\n\n", data); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// resourceFromSubject maps a status subject to the resource it concerns.
// state.<server>.app.<id> → application; state.<server>.db.<id> → database.
func resourceFromSubject(subject string) (invalidateEvent, bool) {
	parts := strings.Split(subject, ".")
	if len(parts) != 4 || parts[0] != "state" {
		return invalidateEvent{}, false
	}
	switch parts[2] {
	case "app":
		return invalidateEvent{Resource: "application", ID: parts[3]}, true
	case "db":
		return invalidateEvent{Resource: "database", ID: parts[3]}, true
	default:
		return invalidateEvent{}, false
	}
}

// visibilityCache answers "may this user see this resource?" once per resource
// per connection. A panel owner sees everything; others are checked against the
// resource's owning team, reusing the same resolvers the request routes use.
type visibilityCache struct {
	api  *API
	user domain.User
	seen map[string]bool
}

func (v *visibilityCache) visible(ctx context.Context, ev invalidateEvent) bool {
	if v.user.Role == domain.RoleOwner {
		return true // panel-owner bypass (teams-and-roles.md §1)
	}
	key := ev.Resource + ":" + ev.ID
	if allowed, ok := v.seen[key]; ok {
		return allowed
	}
	allowed := v.resolve(ctx, ev)
	v.seen[key] = allowed
	return allowed
}

func (v *visibilityCache) resolve(ctx context.Context, ev invalidateEvent) bool {
	if v.api.deps.Teams == nil {
		return false // fail closed: no authorizer, no visibility
	}
	var projectID string
	var err error
	switch ev.Resource {
	case "application":
		projectID, err = v.api.projectIDForApplication(ctx, ev.ID)
	case "database":
		projectID, err = v.api.projectIDForDatabase(ctx, ev.ID)
	default:
		return false
	}
	if err != nil {
		return false // resource gone or unresolvable → not visible
	}
	role, err := v.api.deps.Teams.RoleForProject(ctx, v.user, projectID)
	return err == nil && role != ""
}
