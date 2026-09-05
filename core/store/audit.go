package store

// Audit log persistence (audit-log.md §4, §7). Domain types in, domain types
// out; pgx/pgtype stays inside this package.
//
// There is no update path here, deliberately: the log is evidence. The only
// mutation is PurgeAuditEvents, which removes rows past the retention horizon
// and nothing else.

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store/db"
)

// AuditFilter is one page request against the log. The first four fields are
// the caller's VISIBILITY — resolved by core/audit from the viewer's panel role
// and team memberships, never taken from the request — and the rest are the
// operator's filters, applied inside what they may see (spec §5).
type AuditFilter struct {
	// AllScopes is the panel-owner bypass: every row, every team.
	AllScopes bool
	// PanelScope adds the rows that belong to no team (a server, a user, the
	// mail/DNS/TLS settings). Panel admins and above.
	PanelScope bool
	// TeamIDs are the teams the viewer belongs to.
	TeamIDs []string
	// ViewerID makes a caller's own actions visible to them whatever scope
	// those actions landed in.
	ViewerID string

	TeamID     string
	ProjectID  string
	ResourceID string
	// Action matches exactly, or matches a whole family by prefix: "deploy"
	// selects every "deploy.*" verb.
	Action string
	// Actor matches an actor's user id or its label snapshot (an email).
	Actor   string
	Outcome string
	// Since bounds the window; the zero time means unbounded.
	Since time.Time
	// Before is the id of the last event on the previous page (seek cursor on
	// (at, id) DESC). An id the viewer may not see yields an empty page.
	Before string
	Limit  int32
}

// InsertAuditEvent writes one event, resolving the ownership chain in the same
// statement from whichever link the caller knew (spec §4).
func (s *Store) InsertAuditEvent(ctx context.Context, e domain.AuditEvent) (domain.AuditEvent, error) {
	detail, err := marshalAuditDetail(e.Detail)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	row, err := s.q.InsertAuditEvent(ctx, db.InsertAuditEventParams{
		ID:            e.ID,
		Action:        e.Action,
		Outcome:       e.Outcome,
		ActorKind:     e.Actor.Kind,
		ActorUserID:   textOrNull(e.Actor.UserID),
		ActorTokenID:  textOrNull(e.Actor.TokenID),
		ActorLabel:    e.Actor.Label,
		ResourceKind:  e.Resource.Kind,
		ResourceID:    e.Resource.ID,
		ResourceName:  e.Resource.Name,
		TeamID:        textOrNull(e.TeamID),
		ProjectID:     textOrNull(e.ProjectID),
		EnvironmentID: textOrNull(e.EnvironmentID),
		Detail:        detail,
		TraceID:       e.TraceID,
		ClientIp:      e.ClientIP,
	})
	if err != nil {
		return domain.AuditEvent{}, fmt.Errorf("store: inserting audit event: %w", err)
	}
	return auditEventFromRow(row)
}

// GetAuditEvent reads one event by id. Visibility is decided by core/audit
// against the returned row, so this stays a plain lookup.
func (s *Store) GetAuditEvent(ctx context.Context, id string) (domain.AuditEvent, error) {
	row, err := s.q.GetAuditEvent(ctx, id)
	if err != nil {
		return domain.AuditEvent{}, wrap("getting audit event", err)
	}
	return auditEventFromRow(row)
}

// ListAuditEvents returns one page, newest first.
func (s *Store) ListAuditEvents(ctx context.Context, f AuditFilter) ([]domain.AuditEvent, error) {
	teamIDs := f.TeamIDs
	if teamIDs == nil {
		teamIDs = []string{}
	}
	since := pgtype.Timestamptz{}
	if !f.Since.IsZero() {
		since = tsFromTime(f.Since)
	}
	rows, err := s.q.ListAuditEvents(ctx, db.ListAuditEventsParams{
		AllScopes:  f.AllScopes,
		PanelScope: f.PanelScope,
		TeamIds:    teamIDs,
		ViewerID:   f.ViewerID,
		TeamID:     f.TeamID,
		ProjectID:  f.ProjectID,
		ResourceID: f.ResourceID,
		Action:     f.Action,
		Actor:      f.Actor,
		Outcome:    f.Outcome,
		SinceSet:   !f.Since.IsZero(),
		Since:      since,
		Before:     f.Before,
		RowLimit:   f.Limit,
	})
	if err != nil {
		return nil, fmt.Errorf("store: listing audit events: %w", err)
	}
	out := make([]domain.AuditEvent, 0, len(rows))
	for _, r := range rows {
		ev, err := auditEventFromRow(db.AuditEvent(r))
		if err != nil {
			return nil, err
		}
		out = append(out, ev)
	}
	return out, nil
}

// PurgeAuditEvents deletes up to limit events older than cutoff and reports how
// many it removed. Bounded on purpose: the caller loops until a short batch
// comes back, so a long backlog drains in steps (spec §8).
func (s *Store) PurgeAuditEvents(ctx context.Context, cutoff time.Time, limit int32) (int64, error) {
	n, err := s.q.PurgeAuditEvents(ctx, db.PurgeAuditEventsParams{
		Cutoff:   tsFromTime(cutoff),
		RowLimit: limit,
	})
	if err != nil {
		return 0, fmt.Errorf("store: purging audit events: %w", err)
	}
	return n, nil
}

// marshalAuditDetail encodes the structured extras. An absent map is stored as
// the empty object rather than SQL NULL, so every reader gets an object.
func marshalAuditDetail(d map[string]any) ([]byte, error) {
	if len(d) == 0 {
		return []byte("{}"), nil
	}
	raw, err := json.Marshal(d)
	if err != nil {
		return nil, fmt.Errorf("store: encoding audit detail: %w", err)
	}
	return raw, nil
}

func auditEventFromRow(r db.AuditEvent) (domain.AuditEvent, error) {
	detail := map[string]any{}
	if len(r.Detail) > 0 {
		if err := json.Unmarshal(r.Detail, &detail); err != nil {
			return domain.AuditEvent{}, fmt.Errorf("store: decoding audit detail for %s: %w", r.ID, err)
		}
	}
	return domain.AuditEvent{
		ID:      r.ID,
		At:      r.At.Time,
		Action:  r.Action,
		Outcome: r.Outcome,
		Actor: domain.AuditActor{
			Kind:    r.ActorKind,
			UserID:  r.ActorUserID.String,
			TokenID: r.ActorTokenID.String,
			Label:   r.ActorLabel,
		},
		Resource: domain.AuditResource{
			Kind: r.ResourceKind,
			ID:   r.ResourceID,
			Name: r.ResourceName,
		},
		TeamID:        r.TeamID.String,
		ProjectID:     r.ProjectID.String,
		EnvironmentID: r.EnvironmentID.String,
		Detail:        detail,
		TraceID:       r.TraceID,
		ClientIP:      r.ClientIp,
	}, nil
}

// textOrNull maps the empty string to SQL NULL. Every optional column in
// audit_events is "absent" rather than "empty" — a NULL team_id is what makes a
// row panel-scoped, and an empty string would be a team nobody belongs to.
func textOrNull(s string) pgtype.Text {
	if s == "" {
		return pgtype.Text{}
	}
	return pgtype.Text{String: s, Valid: true}
}
