// Package audit is the panel's audit log (docs/features/audit-log.md): one
// immutable row per sensitive action — who did what to which resource, from
// where, and whether it worked.
//
// It holds three things:
//
//   - the vocabulary (actions.go): the closed set of dotted verbs and resource
//     kinds, so a filter can rely on what it will find;
//   - the write path (Record): validation, secret-key stripping, bounds, and
//     the id — the single place a row is minted;
//   - the read path (List/Get): the caller's visibility resolved from their
//     panel role and team memberships, applied BEFORE any filter they supplied.
//
// What this package deliberately does NOT do is decide whether the audited
// action itself is allowed. Authorization happens where the action happens; the
// log records the outcome either way, including a refusal (canvas 13t: "every
// failure is in the audit log").
package audit

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
)

// ErrUnknownAction is returned by Record for a verb outside the closed
// vocabulary. It is a programmer error — every call site uses a constant — and
// it is loud rather than silent because a row nobody can filter for is worse
// than no row.
var ErrUnknownAction = errors.New("audit: unknown action")

// ValidationError is a malformed read request (a bad limit, an unparseable
// timestamp); handlers map it to 400.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return "audit: " + e.Msg }

func invalid(msg string) error { return &ValidationError{Msg: msg} }

// Bounds. An audit row is a record of an action, not a place to put data: the
// detail is small, and every snapshot string is capped so one enormous name
// cannot bloat every page that lists it.
const (
	maxDetailBytes   = 4096
	maxDetailValue   = 1024
	maxLabelLen      = 320 // an email address at its RFC maximum
	maxResourceName  = 200
	defaultPageLimit = 50
	maxPageLimit     = 200
	// purgeBatch bounds one retention DELETE so a long backlog drains in steps
	// rather than under one lock (§8).
	purgeBatch = 1000
)

// secretKeys are detail key names that must never carry a value. Call sites
// already pass key NAMES and identifiers rather than secrets (§6); this is the
// second line of defence, so a future call site cannot quietly turn the audit
// log into the one place a sealed value appears in plaintext.
//
// Matching is on the WHOLE key, never a substring: `key` is the name of an
// application env var and belongs in the detail, while `value` is its content
// and never does.
var secretKeys = map[string]bool{
	"value":       true,
	"password":    true,
	"secret":      true,
	"plaintext":   true,
	"token":       true,
	"private_key": true,
	"api_key":     true,
	"passphrase":  true,
	"credential":  true,
}

// Store is the persistence this package needs (consumer-defined; *store.Store
// satisfies it).
type Store interface {
	InsertAuditEvent(ctx context.Context, e domain.AuditEvent) (domain.AuditEvent, error)
	GetAuditEvent(ctx context.Context, id string) (domain.AuditEvent, error)
	ListAuditEvents(ctx context.Context, f store.AuditFilter) ([]domain.AuditEvent, error)
	PurgeAuditEvents(ctx context.Context, cutoff time.Time, limit int32) (int64, error)
	// ListTeamsByUser resolves the viewer's tenancy. It is the same query the
	// account screen runs, reused rather than duplicated.
	ListTeamsByUser(ctx context.Context, userID string) ([]domain.TeamWithRole, error)
}

// Service is the audit log's write and read surface.
type Service struct {
	store Store
	// retention is how long rows are kept; zero or negative means forever.
	retention time.Duration
	log       *slog.Logger
	// now is injected so the retention cutoff is deterministic in tests
	// (ENGINEERING rule 9).
	now func() time.Time
}

// NewService wires the service. retention of zero keeps events forever.
func NewService(st Store, retention time.Duration, log *slog.Logger) *Service {
	return &Service{store: st, retention: retention, log: log, now: time.Now}
}

// Retention reports the configured horizon, so the wiring can log it once and
// a caller can say "kept for 90 days" without re-reading the environment.
func (s *Service) Retention() time.Duration { return s.retention }

// ─── Write path (§4) ────────────────────────────────────────────────────────

// Entry is one action as its caller knows it. Everything the caller does not
// know is left empty: the ownership chain is completed in SQL from whichever
// link is present, and Outcome defaults to success.
type Entry struct {
	Action   string
	Outcome  string
	Actor    domain.AuditActor
	Resource domain.AuditResource
	// TeamID/ProjectID/EnvironmentID — supply the most specific one known. A
	// handler that destroys its own ownership chain (deleting a project) must
	// supply TeamID itself, because there is nothing left to resolve from.
	TeamID        string
	ProjectID     string
	EnvironmentID string
	Detail        map[string]any
	TraceID       string
	ClientIP      string
}

// Record writes one event. It is the only way a row is minted.
//
// The write is synchronous with the caller, deliberately: an audit row that
// races its own action would order two entries by scheduling luck, and the
// destructive-confirm dialog's promise ("audit-logged with your name") is only
// true if the row is there when the response is.
func (s *Service) Record(ctx context.Context, e Entry) (domain.AuditEvent, error) {
	if !ValidAction(e.Action) {
		return domain.AuditEvent{}, fmt.Errorf("%w: %q", ErrUnknownAction, e.Action)
	}
	outcome := e.Outcome
	if outcome == "" {
		outcome = domain.AuditSuccess
	}
	if outcome != domain.AuditSuccess && outcome != domain.AuditFailure {
		return domain.AuditEvent{}, fmt.Errorf("audit: unknown outcome %q", outcome)
	}
	actor := e.Actor
	if actor.Kind == "" {
		actor.Kind = domain.AuditActorSystem
	}
	actor.Label = truncate(actor.Label, maxLabelLen)
	resource := e.Resource
	if resource.Kind == "" {
		return domain.AuditEvent{}, fmt.Errorf("audit: %s has no resource kind", e.Action)
	}
	resource.Name = truncate(resource.Name, maxResourceName)

	ev := domain.AuditEvent{
		ID:            ids.New(ids.PrefixAuditEvent),
		Action:        e.Action,
		Outcome:       outcome,
		Actor:         actor,
		Resource:      resource,
		TeamID:        e.TeamID,
		ProjectID:     e.ProjectID,
		EnvironmentID: e.EnvironmentID,
		Detail:        s.sanitize(e.Action, e.Detail),
		TraceID:       e.TraceID,
		ClientIP:      e.ClientIP,
	}
	return s.store.InsertAuditEvent(ctx, ev)
}

// sanitize strips secret-named keys, bounds every string value, and drops the
// whole map if the encoded form is still too large. It never fails: a detail
// that cannot be stored must not cost the row that carries who did what.
func (s *Service) sanitize(action string, in map[string]any) map[string]any {
	if len(in) == 0 {
		return nil
	}
	out := make(map[string]any, len(in))
	for k, v := range in {
		if secretKeys[k] {
			s.log.Warn("audit detail key refused", "action", action, "key", k)
			continue
		}
		if str, ok := v.(string); ok {
			out[k] = truncate(str, maxDetailValue)
			continue
		}
		out[k] = v
	}
	raw, err := json.Marshal(out)
	if err != nil {
		s.log.Warn("audit detail is not encodable — dropped", "action", action, "error", err)
		return map[string]any{"detail_dropped": "not encodable"}
	}
	if len(raw) > maxDetailBytes {
		s.log.Warn("audit detail too large — dropped", "action", action, "bytes", len(raw))
		return map[string]any{"detail_dropped": "too large"}
	}
	return out
}

// Resource builds the resource half of an Entry. It exists so a call site
// stays one statement — `audit.Resource(audit.ResourceApplication, app.ID,
// app.Name)` — rather than three lines of struct literal at every one of the
// fifty-odd places an action is recorded.
func Resource(kind, id, name string) domain.AuditResource {
	return domain.AuditResource{Kind: kind, ID: id, Name: name}
}

// ─── Read path (§5) ─────────────────────────────────────────────────────────

// Query is one page request, as the operator asked for it. Every field is a
// narrowing filter applied INSIDE the caller's visibility — none of them can
// widen it.
type Query struct {
	TeamID     string
	ProjectID  string
	ResourceID string
	// Action is an exact verb ("deploy.started") or a family ("deploy").
	Action string
	// Actor is a user id or the email label recorded at the time.
	Actor   string
	Outcome string
	Since   time.Time
	Before  string
	Limit   int
}

// Page is one page of the log plus the cursor for the next.
type Page struct {
	Events []domain.AuditEvent
	// NextBefore is the id to pass as `before` for the following page; empty
	// when this page is the end of the log.
	NextBefore string
}

// List returns the newest page visible to viewer.
func (s *Service) List(ctx context.Context, viewer domain.User, q Query) (Page, error) {
	if q.Outcome != "" && q.Outcome != domain.AuditSuccess && q.Outcome != domain.AuditFailure {
		return Page{}, invalid("outcome must be success or failure")
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = defaultPageLimit
	case limit > maxPageLimit:
		limit = maxPageLimit
	}
	f, err := s.visibility(ctx, viewer)
	if err != nil {
		return Page{}, err
	}
	f.TeamID = q.TeamID
	f.ProjectID = q.ProjectID
	f.ResourceID = q.ResourceID
	f.Action = q.Action
	f.Actor = q.Actor
	f.Outcome = q.Outcome
	f.Since = q.Since
	f.Before = q.Before
	f.Limit = int32(limit) //nolint:gosec // clamped to maxPageLimit above

	events, err := s.store.ListAuditEvents(ctx, f)
	if err != nil {
		return Page{}, err
	}
	page := Page{Events: events}
	// A full page means there may be more; a short one is the end. This can
	// hand out one cursor that turns out to lead to an empty page, which is
	// cheaper than counting the whole log on every request.
	if len(events) == limit {
		page.NextBefore = events[len(events)-1].ID
	}
	return page, nil
}

// Get returns one event if viewer may see it. An event outside their
// visibility is store.ErrNotFound — the same answer as one that does not exist,
// so the log cannot be probed for what happened in another team (§5).
func (s *Service) Get(ctx context.Context, viewer domain.User, id string) (domain.AuditEvent, error) {
	ev, err := s.store.GetAuditEvent(ctx, id)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	f, err := s.visibility(ctx, viewer)
	if err != nil {
		return domain.AuditEvent{}, err
	}
	if !visible(ev, f) {
		return domain.AuditEvent{}, store.ErrNotFound
	}
	return ev, nil
}

// visibility resolves what viewer may see. It is the only place that decision
// is made, and it reads the viewer's own record — never anything from the
// request (§5).
func (s *Service) visibility(ctx context.Context, viewer domain.User) (store.AuditFilter, error) {
	f := store.AuditFilter{ViewerID: viewer.ID, TeamIDs: []string{}}
	// A panel owner is already an implicit owner of every team
	// (teams.RoleInTeam); the log follows the same bypass rather than inventing
	// a second answer to "who is a superadmin".
	if viewer.Role == domain.RoleOwner {
		f.AllScopes = true
		return f, nil
	}
	// A panel admin additionally reads the rows that belong to no team: a
	// server, a user, the mail/DNS/TLS settings. Those are the actions that
	// role already performs, so it is the role that must be able to review
	// them.
	f.PanelScope = domain.RoleRank(viewer.Role) >= domain.RoleRank(domain.RoleAdmin)
	teams, err := s.store.ListTeamsByUser(ctx, viewer.ID)
	if err != nil {
		return store.AuditFilter{}, fmt.Errorf("audit: resolving viewer teams: %w", err)
	}
	for _, t := range teams {
		f.TeamIDs = append(f.TeamIDs, t.ID)
	}
	return f, nil
}

// visible mirrors the SQL predicate in ListAuditEvents for a single row, so Get
// and List can never disagree about what a caller may see.
func visible(ev domain.AuditEvent, f store.AuditFilter) bool {
	switch {
	case f.AllScopes:
		return true
	case ev.Actor.UserID != "" && ev.Actor.UserID == f.ViewerID:
		return true
	case ev.TeamID == "":
		return f.PanelScope
	}
	for _, id := range f.TeamIDs {
		if id == ev.TeamID {
			return true
		}
	}
	return false
}

// ─── Retention (§8) ─────────────────────────────────────────────────────────

// RunRetention deletes events past the horizon on a fixed interval until ctx is
// done. It is one owned goroutine started by the wiring (ENGINEERING rule 7).
//
// With retention disabled it returns immediately rather than ticking forever
// over a cutoff that can never match — "keep everything" should cost nothing.
func (s *Service) RunRetention(ctx context.Context, every time.Duration, log *slog.Logger) {
	if s.retention <= 0 {
		log.Info("audit retention disabled — events are kept indefinitely")
		return
	}
	log.Info("audit retention active", "retention", s.retention.String(), "interval", every.String())
	// Sweep once at boot: a panel that is restarted more often than the
	// interval would otherwise never purge at all.
	s.purgeAndLog(ctx, log)
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.purgeAndLog(ctx, log)
		}
	}
}

func (s *Service) purgeAndLog(ctx context.Context, log *slog.Logger) {
	n, err := s.Purge(ctx)
	if err != nil {
		log.Error("purging audit events", "error", err)
		return
	}
	if n > 0 {
		log.Info("purged audit events past retention", "deleted", n, "retention", s.retention.String())
	}
}

// Purge deletes every event older than the retention horizon, in bounded
// batches, and reports the total. It stops early when ctx is done, so shutdown
// does not wait on a long backlog.
func (s *Service) Purge(ctx context.Context) (int64, error) {
	if s.retention <= 0 {
		return 0, nil
	}
	cutoff := s.now().Add(-s.retention)
	var total int64
	for {
		if err := ctx.Err(); err != nil {
			return total, nil //nolint:nilerr // a cancelled sweep is not a failure
		}
		n, err := s.store.PurgeAuditEvents(ctx, cutoff, purgeBatch)
		if err != nil {
			return total, err
		}
		total += n
		if n < purgeBatch {
			return total, nil
		}
	}
}

// truncate bounds a snapshot string to max BYTES, marking that it was cut so a
// reader is never misled into thinking a clipped name is the whole one.
//
// Two things it must never do, because both end in a row Postgres refuses and
// a refused INSERT is a MISSING audit entry:
//
//   - cut mid-rune. Snapshots are caller-supplied — the address typed at a
//     failed sign-in, a webhook URL — so a byte slice would let anyone whose
//     name is longer than the cap and not ASCII make their own failures vanish
//     from the log. The cut lands on a rune boundary, and the ellipsis's three
//     bytes are budgeted INSIDE max so the result still fits the column.
//   - pass through invalid UTF-8. A percent-decoded path segment can carry
//     arbitrary bytes; coerce them rather than lose the record of what
//     happened.
func truncate(s string, max int) string {
	if !utf8.ValidString(s) {
		s = strings.ToValidUTF8(s, "\uFFFD")
	}
	if max <= 0 {
		return ""
	}
	if len(s) <= max {
		return s
	}
	mark := "…"
	budget := max - len(mark)
	if budget < 0 {
		// A cap too small to hold the marker clips silently rather than
		// overflowing it.
		mark, budget = "", max
	}
	cut := 0
	for i := range s {
		if i > budget {
			break
		}
		cut = i
	}
	return s[:cut] + mark
}
