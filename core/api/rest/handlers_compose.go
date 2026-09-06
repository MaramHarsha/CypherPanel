package rest

// Compose Stacks (compose-stacks.md §7).
//
// The authorization story is one split: writing the FILE is team admin,
// deploying one is a member. A compose file can ask for `privileged: true` and
// a host mount, which is root on the node and something an Application cannot
// express — so it must not be reachable at the rank that deploys an
// application. Once an admin has written and reviewed it, redeploying grants
// nothing new, so a member (and a CI token with `deploy`) can do that.

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/compose"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// ComposeService is the Compose Stack surface (consumer-defined;
// *compose.Service satisfies it).
type ComposeService interface {
	Create(ctx context.Context, envID string, in compose.Input) (domain.ComposeStack, error)
	Update(ctx context.Context, id string, in compose.UpdateInput) (domain.ComposeStack, error)
	Get(ctx context.Context, id string) (domain.ComposeStack, error)
	List(ctx context.Context, envID string) ([]domain.ComposeStack, error)
	Delete(ctx context.Context, id string, deleteVolumes bool) error
	Deploy(ctx context.Context, id string) (domain.ComposeStack, error)
	Rollback(ctx context.Context, id, revisionID string) (domain.ComposeStack, error)
	Revisions(ctx context.Context, id string) ([]domain.ComposeRevision, error)
	File(ctx context.Context, id string) (domain.ComposeRevision, error)
	SetEnvVar(ctx context.Context, id, key, value string) error
	EnvKeys(ctx context.Context, id string) ([]string, error)
	DeleteEnvVar(ctx context.Context, id, key string) error
}

type composeStackDTO struct {
	ID            string `json:"id"`
	EnvironmentID string `json:"environment_id"`
	Name          string `json:"name"`
	ServerID      string `json:"server_id"`
	Route         struct {
		Domain  string `json:"domain"`
		Service string `json:"service"`
		Port    int    `json:"port"`
		HTTPS   bool   `json:"https"`
	} `json:"route"`
	DesiredRevisionID  *string `json:"desired_revision_id"`
	Status             string  `json:"status"`
	StatusDetail       string  `json:"status_detail"`
	ObservedRevisionID string  `json:"observed_revision_id"`
	CreatedAt          string  `json:"created_at"`
}

func toComposeStackDTO(s domain.ComposeStack) composeStackDTO {
	var dto composeStackDTO
	dto.ID, dto.EnvironmentID, dto.Name, dto.ServerID = s.ID, s.EnvironmentID, s.Name, s.ServerID
	dto.Route.Domain, dto.Route.Service = s.Route.Domain, s.Route.Service
	dto.Route.Port, dto.Route.HTTPS = s.Route.Port, s.Route.HTTPS
	dto.DesiredRevisionID = s.DesiredRevisionID
	dto.Status, dto.StatusDetail = s.Status, s.StatusDetail
	dto.ObservedRevisionID = s.ObservedRevisionID
	dto.CreatedAt = s.CreatedAt.UTC().Format(time.RFC3339)
	return dto
}

type composeRevisionDTO struct {
	ID          string `json:"id"`
	StackID     string `json:"stack_id"`
	ComposeYAML string `json:"compose_yaml"`
	CreatedAt   string `json:"created_at"`
}

func toComposeRevisionDTO(r domain.ComposeRevision) composeRevisionDTO {
	return composeRevisionDTO{
		ID: r.ID, StackID: r.StackID, ComposeYAML: r.ComposeYAML,
		CreatedAt: r.CreatedAt.UTC().Format(time.RFC3339),
	}
}

type composeRouteReq struct {
	Domain  string `json:"domain"`
	Service string `json:"service"`
	Port    int    `json:"port"`
	HTTPS   *bool  `json:"https"`
}

func (r composeRouteReq) toDomain() domain.ComposeRoute {
	https := true
	if r.HTTPS != nil {
		https = *r.HTTPS
	}
	return domain.ComposeRoute{Domain: r.Domain, Service: r.Service, Port: r.Port, HTTPS: https}
}

type createComposeStackRequest struct {
	Name        string            `json:"name"`
	ServerID    string            `json:"server_id"`
	ComposeYAML string            `json:"compose_yaml"`
	Route       composeRouteReq   `json:"route"`
	EnvVars     map[string]string `json:"env_vars"`
}

type patchComposeStackRequest struct {
	Name        *string          `json:"name"`
	ComposeYAML *string          `json:"compose_yaml"`
	Route       *composeRouteReq `json:"route"`
}

type rollbackComposeRequest struct {
	RevisionID string `json:"revision_id"`
}

// composeStack resolves the stack and authorizes the caller at min rank in its
// project.
func (a *API) composeStack(w http.ResponseWriter, r *http.Request, user domain.User, min string) (domain.ComposeStack, bool) {
	if a.deps.Compose == nil {
		writeError(w, http.StatusNotImplemented, "compose stacks are not enabled")
		return domain.ComposeStack{}, false
	}
	id := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, min, func(ctx context.Context) (string, error) {
		return a.projectIDForComposeStack(ctx, id)
	}) {
		return domain.ComposeStack{}, false
	}
	stack, err := a.deps.Compose.Get(r.Context(), id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			writeError(w, http.StatusNotFound, "compose stack not found")
			return domain.ComposeStack{}, false
		}
		a.deps.Log.Error("getting compose stack", "stack_id", id, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the compose stack")
		return domain.ComposeStack{}, false
	}
	return stack, true
}

func (a *API) handleListComposeStacks(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Compose == nil {
		writeError(w, http.StatusNotImplemented, "compose stacks are not enabled")
		return
	}
	envID := r.PathValue("id")
	if !a.authorizeResolved(w, r, user, domain.RoleMember, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	list, err := a.deps.Compose.List(r.Context(), envID)
	if err != nil {
		a.deps.Log.Error("listing compose stacks", "environment_id", envID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list compose stacks")
		return
	}
	out := make([]composeStackDTO, 0, len(list))
	for _, s := range list {
		out = append(out, toComposeStackDTO(s))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleCreateComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Compose == nil {
		writeError(w, http.StatusNotImplemented, "compose stacks are not enabled")
		return
	}
	envID := r.PathValue("id")
	// Admin, not member: this route carries the file (§7).
	if !a.authorizeResolved(w, r, user, domain.RoleAdmin, func(ctx context.Context) (string, error) {
		return a.projectIDForEnvironment(ctx, envID)
	}) {
		return
	}
	var req createComposeStackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	stack, err := a.deps.Compose.Create(r.Context(), envID, compose.Input{
		Name: req.Name, ServerID: req.ServerID, ComposeYAML: req.ComposeYAML,
		Route: req.Route.toDomain(), EnvVars: req.EnvVars,
	})
	if !a.writeComposeError(w, "creating compose stack", err) {
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackCreated,
		Resource:      audit.Resource(audit.ResourceComposeStack, stack.ID, stack.Name),
		EnvironmentID: stack.EnvironmentID,
		Detail:        map[string]any{"server_id": stack.ServerID},
	})
	writeJSON(w, http.StatusCreated, toComposeStackDTO(stack))
}

func (a *API) handleGetComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, toComposeStackDTO(stack))
}

// handleGetComposeFile returns the file a deploy would ship — the newest
// revision, not the row, so what is shown and what would run cannot differ.
func (a *API) handleGetComposeFile(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	rev, err := a.deps.Compose.File(r.Context(), stack.ID)
	if errors.Is(err, compose.ErrNeverDeployed) || errors.Is(err, store.ErrNotFound) {
		writeError(w, http.StatusNotFound, "this stack has no compose file yet")
		return
	}
	if err != nil {
		a.deps.Log.Error("reading compose file", "stack_id", stack.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not read the compose file")
		return
	}
	writeJSON(w, http.StatusOK, toComposeRevisionDTO(rev))
}

func (a *API) handlePatchComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	// Read the body first: whether this needs admin depends on WHAT it changes,
	// and the file is the part that does.
	if a.deps.Compose == nil {
		writeError(w, http.StatusNotImplemented, "compose stacks are not enabled")
		return
	}
	var req patchComposeStackRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rank := domain.RoleMember
	if req.ComposeYAML != nil || req.Route != nil {
		rank = domain.RoleAdmin
	}
	stack, ok := a.composeStack(w, r, user, rank)
	if !ok {
		return
	}
	in := compose.UpdateInput{Name: req.Name, ComposeYAML: req.ComposeYAML}
	if req.Route != nil {
		route := req.Route.toDomain()
		in.Route = &route
	}
	updated, err := a.deps.Compose.Update(r.Context(), stack.ID, in)
	if !a.writeComposeError(w, "updating compose stack", err) {
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackUpdated,
		Resource:      audit.Resource(audit.ResourceComposeStack, updated.ID, updated.Name),
		EnvironmentID: updated.EnvironmentID,
		// That the file changed, never its content: a compose file can carry an
		// inline secret an operator put there, and the audit log is not the
		// place for it to become permanent (§7).
		Detail: map[string]any{"file_changed": req.ComposeYAML != nil},
	})
	writeJSON(w, http.StatusOK, toComposeStackDTO(updated))
}

func (a *API) handleDeleteComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleAdmin)
	if !ok {
		return
	}
	// Convergence never removes a volume, so this flag is the only way a
	// stack's data goes — and it is never the default.
	deleteVolumes := r.URL.Query().Get("delete_volumes") == "true"
	if err := a.deps.Compose.Delete(r.Context(), stack.ID, deleteVolumes); err != nil {
		a.deps.Log.Error("deleting compose stack", "stack_id", stack.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the compose stack")
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackDeleted,
		Resource:      audit.Resource(audit.ResourceComposeStack, stack.ID, stack.Name),
		EnvironmentID: stack.EnvironmentID,
		Detail:        map[string]any{"delete_volumes": deleteVolumes},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeployComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	entry := audit.Entry{
		Action:        audit.ActionComposeStackDeployed,
		Resource:      audit.Resource(audit.ResourceComposeStack, stack.ID, stack.Name),
		EnvironmentID: stack.EnvironmentID,
	}
	deployed, err := a.deps.Compose.Deploy(r.Context(), stack.ID)
	if errors.Is(err, compose.ErrNeverDeployed) {
		a.auditFailed(r, entry, "the stack has no compose file")
		writeError(w, http.StatusConflict, "this stack has no compose file yet")
		return
	}
	if !a.writeComposeError(w, "deploying compose stack", err) {
		return
	}
	a.audit(r, entry)
	writeJSON(w, http.StatusAccepted, toComposeStackDTO(deployed))
}

func (a *API) handleRollbackComposeStack(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	var req rollbackComposeRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	rolled, err := a.deps.Compose.Rollback(r.Context(), stack.ID, req.RevisionID)
	if !a.writeComposeError(w, "rolling back compose stack", err) {
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackRolledBack,
		Resource:      audit.Resource(audit.ResourceComposeStack, rolled.ID, rolled.Name),
		EnvironmentID: rolled.EnvironmentID,
		Detail:        map[string]any{"revision_id": req.RevisionID},
	})
	writeJSON(w, http.StatusAccepted, toComposeStackDTO(rolled))
}

func (a *API) handleListComposeRevisions(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	revs, err := a.deps.Compose.Revisions(r.Context(), stack.ID)
	if err != nil {
		a.deps.Log.Error("listing compose revisions", "stack_id", stack.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list revisions")
		return
	}
	out := make([]composeRevisionDTO, 0, len(revs))
	for _, rev := range revs {
		out = append(out, toComposeRevisionDTO(rev))
	}
	writeJSON(w, http.StatusOK, out)
}

func (a *API) handleComposeStackLogs(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	a.streamRuntimeLogSSE(w, r, subjects.RuntimeLog(stack.ServerID, stack.ID))
}

// ─── env vars ───────────────────────────────────────────────────────────────

func (a *API) handleListComposeEnvVars(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	keys, err := a.deps.Compose.EnvKeys(r.Context(), stack.ID)
	if err != nil {
		a.deps.Log.Error("listing compose env vars", "stack_id", stack.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not list variables")
		return
	}
	// Keys only; a value is never read back (rule 20).
	writeJSON(w, http.StatusOK, map[string]any{"keys": keys})
}

func (a *API) handleSetComposeEnvVar(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	var req struct {
		Value string `json:"value"`
	}
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	key := r.PathValue("key")
	if err := a.deps.Compose.SetEnvVar(r.Context(), stack.ID, key, req.Value); err != nil {
		if !a.writeComposeError(w, "setting compose variable", err) {
			return
		}
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackUpdated,
		Resource:      audit.Resource(audit.ResourceComposeStack, stack.ID, stack.Name),
		EnvironmentID: stack.EnvironmentID,
		// The key, never the value (rule 20).
		Detail: map[string]any{"env_var": key},
	})
	w.WriteHeader(http.StatusNoContent)
}

func (a *API) handleDeleteComposeEnvVar(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	stack, ok := a.composeStack(w, r, user, domain.RoleMember)
	if !ok {
		return
	}
	key := r.PathValue("key")
	if err := a.deps.Compose.DeleteEnvVar(r.Context(), stack.ID, key); err != nil {
		a.deps.Log.Error("deleting compose variable", "stack_id", stack.ID, "error", err)
		writeError(w, http.StatusInternalServerError, "could not delete the variable")
		return
	}
	a.audit(r, audit.Entry{
		Action:        audit.ActionComposeStackUpdated,
		Resource:      audit.Resource(audit.ResourceComposeStack, stack.ID, stack.Name),
		EnvironmentID: stack.EnvironmentID,
		Detail:        map[string]any{"env_var_removed": key},
	})
	w.WriteHeader(http.StatusNoContent)
}

// writeComposeError maps service errors and reports whether to continue.
func (a *API) writeComposeError(w http.ResponseWriter, op string, err error) bool {
	var ve *compose.ValidationError
	switch {
	case err == nil:
		return true
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	case errors.Is(err, store.ErrConflict):
		writeError(w, http.StatusConflict, "a compose stack with that name already exists in the environment")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
	return false
}
