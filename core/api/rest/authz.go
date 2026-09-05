package rest

// Authorization helpers (teams-and-roles.md §3): every project-scoped route
// resolves its resource to a project id, looks up the caller's effective role
// in the owning team (panel owners bypass), and compares rank. Non-members get
// 404 — a project you cannot see does not exist (no tenancy probing);
// insufficient rank gets 403.

import (
	"context"
	"errors"
	"net/http"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// requirePanelRole gates panel-level infrastructure routes (servers, deploy
// keys, backup targets, users, team creation). Writes 403 and returns false
// when the caller's panel role ranks below min.
func (a *API) requirePanelRole(w http.ResponseWriter, user domain.User, min string) bool {
	if domain.RoleRank(user.Role) < domain.RoleRank(min) {
		writeError(w, http.StatusForbidden, "insufficient role")
		return false
	}
	return true
}

// requireProjectRole authorizes user against the team owning projectID at
// minimum rank min. Writes the response (404 non-member / 403 low rank / 500)
// and returns false on failure.
func (a *API) requireProjectRole(w http.ResponseWriter, r *http.Request, user domain.User, projectID, min string) bool {
	if a.deps.Teams == nil {
		// Fail closed: authorization missing is never authorization granted.
		writeError(w, http.StatusInternalServerError, "authorization is not configured")
		return false
	}
	// A project-scoped token reaching another project gets the same answer a
	// non-member gets: not found. Anything more specific would let one token
	// enumerate which projects its owner can see, which is exactly the reach
	// the scope exists to remove.
	if scope, scoped := principalScope(r.Context()); scoped && scope != projectID {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	role, err := a.deps.Teams.RoleForProject(r.Context(), user, projectID)
	if err != nil {
		a.deps.Log.Error("resolving project role", "project_id", projectID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return false
	}
	if role == "" {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	if domain.RoleRank(role) < domain.RoleRank(min) {
		writeError(w, http.StatusForbidden, "insufficient role")
		return false
	}
	return true
}

// requireTeamRole authorizes user against teamID directly (the /teams routes).
func (a *API) requireTeamRole(w http.ResponseWriter, r *http.Request, user domain.User, teamID, min string) bool {
	if a.deps.Teams == nil {
		writeError(w, http.StatusInternalServerError, "authorization is not configured")
		return false
	}
	role, err := a.deps.Teams.RoleInTeam(r.Context(), user, teamID)
	if err != nil {
		a.deps.Log.Error("resolving team role", "team_id", teamID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return false
	}
	if role == "" {
		writeError(w, http.StatusNotFound, "not found")
		return false
	}
	if domain.RoleRank(role) < domain.RoleRank(min) {
		writeError(w, http.StatusForbidden, "insufficient role")
		return false
	}
	return true
}

// teamRoleAtLeast is requireTeamRole that also HANDS BACK the role. Routes
// which both gate on a minimum rank and then reason about the caller's own rank
// — issuing an invitation may not exceed it, an access request must ask above
// it — need one resolution, not two, so the answer they enforce and the answer
// they act on cannot come from different reads.
func (a *API) teamRoleAtLeast(w http.ResponseWriter, r *http.Request, user domain.User, teamID, min string) (string, bool) {
	if a.deps.Teams == nil {
		writeError(w, http.StatusInternalServerError, "authorization is not configured")
		return "", false
	}
	role, err := a.deps.Teams.RoleInTeam(r.Context(), user, teamID)
	if err != nil {
		a.deps.Log.Error("resolving team role", "team_id", teamID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return "", false
	}
	if role == "" {
		// A team you cannot see does not exist (teams-and-roles.md §3).
		writeError(w, http.StatusNotFound, "not found")
		return "", false
	}
	if domain.RoleRank(role) < domain.RoleRank(min) {
		writeError(w, http.StatusForbidden, "insufficient role")
		return "", false
	}
	return role, true
}

// teamRoleIn returns the caller's effective role in teamID ("" = none) without
// writing a response — for handlers that branch rather than gate.
func (a *API) teamRoleIn(ctx context.Context, user domain.User, teamID string) (string, error) {
	return a.deps.Teams.RoleInTeam(ctx, user, teamID)
}

// ─── Resource → project resolution (spec §3) ────────────────────────────────
//
// Each resolver walks the ownership chain with the services already in Deps.
// store.ErrNotFound anywhere in the chain is returned as-is: the route's
// normal not-found handling applies (which is also what a non-member sees).

func (a *API) projectIDForEnvironment(ctx context.Context, envID string) (string, error) {
	env, err := a.deps.Projects.GetEnvironment(ctx, envID)
	if err != nil {
		return "", err
	}
	return env.ProjectID, nil
}

func (a *API) projectIDForApplication(ctx context.Context, appID string) (string, error) {
	app, err := a.deps.Applications.Get(ctx, appID)
	if err != nil {
		return "", err
	}
	return a.projectIDForEnvironment(ctx, app.EnvironmentID)
}

func (a *API) projectIDForDeployment(ctx context.Context, depID string) (string, error) {
	dep, err := a.deps.Deployments.GetDeployment(ctx, depID)
	if err != nil {
		return "", err
	}
	return a.projectIDForApplication(ctx, dep.ApplicationID)
}

func (a *API) projectIDForDatabase(ctx context.Context, dbID string) (string, error) {
	if a.deps.Databases == nil {
		return "", store.ErrNotFound
	}
	db, err := a.deps.Databases.Get(ctx, dbID)
	if err != nil {
		return "", err
	}
	return a.projectIDForEnvironment(ctx, db.EnvironmentID)
}

func (a *API) projectIDForPreview(ctx context.Context, previewID string) (string, error) {
	if a.deps.Previews == nil {
		return "", store.ErrNotFound
	}
	p, err := a.deps.Previews.Get(ctx, previewID)
	if err != nil {
		return "", err
	}
	return a.projectIDForApplication(ctx, p.SourceAppID)
}

func (a *API) projectIDForNotifier(ctx context.Context, notifierID string) (string, error) {
	if a.deps.Notifiers == nil {
		return "", store.ErrNotFound
	}
	n, err := a.deps.Notifiers.Get(ctx, notifierID)
	if err != nil {
		return "", err
	}
	return n.ProjectID, nil
}

func (a *API) projectIDForWebhookEndpoint(ctx context.Context, endpointID string) (string, error) {
	if a.deps.WebhookEndpoints == nil {
		return "", store.ErrNotFound
	}
	e, err := a.deps.WebhookEndpoints.Get(ctx, endpointID)
	if err != nil {
		return "", err
	}
	return e.ProjectID, nil
}

// projectIDForWebhookDelivery chains through the delivery's endpoint — a
// delivery is only ever addressable by someone who can see the endpoint that
// produced it (outbound-webhooks.md §6).
func (a *API) projectIDForWebhookDelivery(ctx context.Context, deliveryID string) (string, error) {
	if a.deps.WebhookEndpoints == nil {
		return "", store.ErrNotFound
	}
	d, err := a.deps.WebhookEndpoints.GetDelivery(ctx, deliveryID)
	if err != nil {
		return "", err
	}
	return a.projectIDForWebhookEndpoint(ctx, d.EndpointID)
}

// projectIDForSharedVariable resolves straight to the owning project: a shared
// variable is project-scoped even when narrowed to one environment, and read
// and write both need domain.RoleMember — the rank an application's own env
// vars already require, since they are the same class of secret
// (shared-variables.md §6).
func (a *API) projectIDForSharedVariable(ctx context.Context, id string) (string, error) {
	if a.deps.SharedVariables == nil {
		return "", store.ErrNotFound
	}
	v, err := a.deps.SharedVariables.Get(ctx, id)
	if err != nil {
		return "", err
	}
	return v.ProjectID, nil
}

func (a *API) projectIDForScheduledTask(ctx context.Context, taskID string) (string, error) {
	if a.deps.ScheduledTasks == nil {
		return "", store.ErrNotFound
	}
	t, err := a.deps.ScheduledTasks.Get(ctx, taskID)
	if err != nil {
		return "", err
	}
	return a.projectIDForApplication(ctx, t.ApplicationID)
}

// authorizeResolved wraps the resolve-then-require pattern: resolve projectID
// via resolve, then enforce min. A resolution ErrNotFound writes 404 (the
// resource does not exist — same as the non-member view). Returns false when
// the response has been written.
func (a *API) authorizeResolved(w http.ResponseWriter, r *http.Request, user domain.User, min string,
	resolve func(ctx context.Context) (string, error)) bool {
	projectID, err := resolve(r.Context())
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "not found")
			return false
		}
		a.deps.Log.Error("resolving resource project", "error", err)
		writeError(w, http.StatusInternalServerError, "could not authorize request")
		return false
	}
	return a.requireProjectRole(w, r, user, projectID, min)
}

// principalScope returns the project an API token is confined to, if any. Every
// project-scoped authorization funnels through requireProjectRole, so this is
// the one place the check has to live.
func principalScope(ctx context.Context) (string, bool) {
	p, ok := principalFromContext(ctx)
	if !ok {
		return "", false
	}
	return p.ScopedToProject()
}
