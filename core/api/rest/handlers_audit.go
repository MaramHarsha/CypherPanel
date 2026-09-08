package rest

// The audit log (audit-log.md §5, §7).
//
// This file is two halves that never meet in the middle:
//
//   * the WRITE helper every other handler calls — one line at the end of a
//     sensitive action, taking the actor, the trace id and the client address
//     from the request the hardening middleware already annotated;
//   * the two READ routes, whose tenancy lives entirely in core/audit: the
//     service resolves what the caller may see from their own record, and every
//     query parameter narrows inside that. No parameter can widen it, which is
//     why these routes need no authorization resolver in authz.go.

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// auditWriteTimeout bounds the recording write. It runs on a context detached
// from the request's (below), so it needs a deadline of its own.
const auditWriteTimeout = 5 * time.Second

// audit records one sensitive action. It is deliberately shaped so a call site
// is a single statement after the action it describes.
//
// Three decisions live here:
//
//   - It never fails the request. The action has already happened; answering
//     500 because the *record* of it could not be written would turn a
//     successful deploy into a failed one and invite a retry that does it
//     twice. A failed write is an error-level log line carrying the trace id,
//     which is the loudest thing available that does not lie to the caller.
//   - It is synchronous. A detached goroutine would order entries by
//     scheduling luck, and the destructive-confirm dialog's promise that an
//     action is "audit-logged with your name" is only true if the row is there
//     when the response is.
//   - It runs on a context detached from the request's cancellation. A client
//     that hangs up the instant its DELETE returns must not take the record of
//     that DELETE with it. The values (the trace id) are kept; only the
//     cancellation is dropped, under this file's own timeout.
func (a *API) audit(r *http.Request, e audit.Entry) {
	if a.deps.Audit == nil {
		return
	}
	if e.Actor.Kind == "" {
		e.Actor = a.auditActor(r)
	}
	e.TraceID = traceIDFromContext(r.Context())
	e.ClientIP = a.clientIP(r)

	ctx, cancel := context.WithTimeout(context.WithoutCancel(r.Context()), auditWriteTimeout)
	defer cancel()
	if _, err := a.deps.Audit.Record(ctx, e); err != nil {
		a.deps.Log.Error("recording audit event",
			"action", e.Action,
			"resource_kind", e.Resource.Kind,
			"resource_id", e.Resource.ID,
			"trace_id", e.TraceID,
			"error", err)
	}
}

// auditFailed records a refused action — the same row with outcome `failure`
// and a reason. It is what makes "every failure is in the audit log"
// (canvas 13t) true for the refusals that matter: a wrong password, a rejected
// deploy, an action a role could not perform.
func (a *API) auditFailed(r *http.Request, e audit.Entry, reason string) {
	e.Outcome = domain.AuditFailure
	if e.Detail == nil {
		e.Detail = map[string]any{}
	}
	e.Detail["reason"] = reason
	a.audit(r, e)
}

// auditActor derives the actor from the authenticated principal. A token acts
// as its owner, so the owning user is recorded alongside the token id: the
// question after a leak is both "who does this belong to" and "which credential
// do I revoke".
func (a *API) auditActor(r *http.Request) domain.AuditActor {
	p, ok := principalFromContext(r.Context())
	if !ok {
		return domain.AuditActor{Kind: domain.AuditActorAnonymous}
	}
	kind := domain.AuditActorUser
	if p.Kind == auth.KindAPIToken {
		kind = domain.AuditActorToken
	}
	return domain.AuditActor{
		Kind:    kind,
		UserID:  p.User.ID,
		TokenID: p.TokenID,
		Label:   p.User.Email,
	}
}

// auditUserActor attributes an action to a named user rather than to the
// caller — the sign-in that has no principal yet, and the first-run setup that
// creates the account it is attributed to.
func auditUserActor(u domain.User) domain.AuditActor {
	return domain.AuditActor{Kind: domain.AuditActorUser, UserID: u.ID, Label: u.Email}
}

// ─── read routes ────────────────────────────────────────────────────────────

type auditActorDTO struct {
	// Kind is user | token | agent | system | anonymous.
	Kind string `json:"kind"`
	// UserID is empty for agent, system and anonymous actors. It is a snapshot:
	// the account it names may since have been deleted, and the entry stays.
	UserID string `json:"user_id"`
	// TokenID names the personal access token used, when one was — the
	// credential to revoke.
	TokenID string `json:"token_id"`
	// Label is the email as it read when the action happened, never re-resolved.
	Label string `json:"label"`
}

type auditResourceDTO struct {
	Kind string `json:"kind"`
	ID   string `json:"id"`
	// Name is the snapshot at the time; renaming the resource does not rewrite
	// its history.
	Name string `json:"name"`
}

type auditEventDTO struct {
	ID      string    `json:"id"`
	At      time.Time `json:"at"`
	Action  string    `json:"action"`
	Outcome string    `json:"outcome"`

	Actor    auditActorDTO    `json:"actor"`
	Resource auditResourceDTO `json:"resource"`

	// The ownership chain as it stood. An empty team_id means the action was
	// panel-level and only panel admins can read it.
	TeamID        string `json:"team_id"`
	ProjectID     string `json:"project_id"`
	EnvironmentID string `json:"environment_id"`

	// Detail carries identifiers, key NAMES and reasons — never a secret value.
	Detail map[string]any `json:"detail"`
	// TraceID is the X-Request-Id of the request that performed the action, so
	// the id in a screenshot finds what it did as well as what it logged.
	TraceID  string `json:"trace_id"`
	ClientIP string `json:"client_ip"`
}

func toAuditEventDTO(e domain.AuditEvent) auditEventDTO {
	detail := e.Detail
	if detail == nil {
		detail = map[string]any{}
	}
	return auditEventDTO{
		ID:      e.ID,
		At:      e.At,
		Action:  e.Action,
		Outcome: e.Outcome,
		Actor: auditActorDTO{
			Kind:    e.Actor.Kind,
			UserID:  e.Actor.UserID,
			TokenID: e.Actor.TokenID,
			Label:   e.Actor.Label,
		},
		Resource: auditResourceDTO{
			Kind: e.Resource.Kind,
			ID:   e.Resource.ID,
			Name: e.Resource.Name,
		},
		TeamID:        e.TeamID,
		ProjectID:     e.ProjectID,
		EnvironmentID: e.EnvironmentID,
		Detail:        detail,
		TraceID:       e.TraceID,
		ClientIP:      e.ClientIP,
	}
}

type auditPageDTO struct {
	Events []auditEventDTO `json:"events"`
	// NextBefore is the cursor for the following page; empty at the end of the
	// log.
	NextBefore string `json:"next_before"`
}

// handleListAuditEvents pages the log newest-first, scoped to what the caller
// may see (§5). Filters narrow inside that scope: a team_id the caller does not
// belong to yields an empty page rather than a 403, so the log cannot be probed
// for the existence of another tenant's activity.
func (a *API) handleListAuditEvents(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Audit == nil {
		writeJSON(w, http.StatusOK, auditPageDTO{Events: []auditEventDTO{}})
		return
	}
	q := r.URL.Query()
	limit := 0 // 0 = the service's default
	if raw := q.Get("limit"); raw != "" {
		// ParseInt with an explicit bit size, not Atoi: the service narrows
		// this to int32 for the query's LIMIT, and Atoi's architecture-
		// dependent int silently wraps on that narrowing.
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = int(n)
	}
	var since time.Time
	if raw := q.Get("since"); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "since must be an RFC 3339 timestamp")
			return
		}
		since = t
	}
	page, err := a.deps.Audit.List(r.Context(), user, audit.Query{
		TeamID:     q.Get("team_id"),
		ProjectID:  q.Get("project_id"),
		ResourceID: q.Get("resource_id"),
		Action:     q.Get("action"),
		Actor:      q.Get("actor"),
		Outcome:    q.Get("outcome"),
		Since:      since,
		Before:     q.Get("before"),
		Limit:      limit,
	})
	if err != nil {
		a.writeAuditError(w, "listing audit events", err)
		return
	}
	out := make([]auditEventDTO, 0, len(page.Events))
	for _, e := range page.Events {
		out = append(out, toAuditEventDTO(e))
	}
	writeJSON(w, http.StatusOK, auditPageDTO{Events: out, NextBefore: page.NextBefore})
}

// handleGetAuditEvent returns one entry — the permalink behind a trace id in a
// support conversation. An entry outside the caller's scope is 404, the same
// answer as one that does not exist.
func (a *API) handleGetAuditEvent(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Audit == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	ev, err := a.deps.Audit.Get(r.Context(), user, r.PathValue("id"))
	if err != nil {
		a.writeAuditError(w, "getting audit event", err)
		return
	}
	writeJSON(w, http.StatusOK, toAuditEventDTO(ev))
}

func (a *API) writeAuditError(w http.ResponseWriter, op string, err error) {
	var ve *audit.ValidationError
	switch {
	case errors.As(err, &ve):
		writeError(w, http.StatusBadRequest, ve.Msg)
	case errors.Is(err, store.ErrNotFound):
		writeError(w, http.StatusNotFound, "not found")
	default:
		a.deps.Log.Error(op, "error", err)
		writeError(w, http.StatusInternalServerError, "could not "+op)
	}
}
