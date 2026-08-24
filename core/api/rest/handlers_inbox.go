package rest

// The notification inbox (notification-inbox.md §5, §6). This is the first
// feature here whose authorization is a COLUMN: an item *is* a per-user row, so
// every handler passes the authenticated caller's id into the service and no
// route accepts another user's. There is no projectIDForInboxItem resolver in
// authz.go and no 404-over-403 posture to get wrong — the collection is
// `/inbox`, never `/users/{id}/inbox`, which makes the guarantee syntactic.
//
// API tokens act as their owner (GET → `read`, POST/PUT → `write`), so a token
// reads and clears its owner's inbox and nobody else's. That is not credential
// management, so these routes are not sessionOnly.

import (
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/inbox"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// inboxItemDTO is what the feed renders. `title` is COMPOSED server-side — a
// digest's "Backups: 3/3 succeeded" is assembled from counters that stay out of
// the contract, because a client rendering them into English would be a second
// home for copy and a CLI would get it subtly different (spec §6).
type inboxItemDTO struct {
	ID        string     `json:"id"`
	ProjectID string     `json:"project_id"`
	Kind      string     `json:"kind"`
	Severity  string     `json:"severity"`
	Digest    bool       `json:"digest"`
	Title     string     `json:"title"`
	Body      string     `json:"body"`
	Link      string     `json:"link"`
	LinkLabel string     `json:"link_label"`
	ReadAt    *time.Time `json:"read_at"`
	CreatedAt time.Time  `json:"created_at"`
	UpdatedAt time.Time  `json:"updated_at"`
}

type inboxPageDTO struct {
	Items      []inboxItemDTO `json:"items"`
	NextBefore string         `json:"next_before"`
}

type unreadCountDTO struct {
	Unread int64 `json:"unread"`
}

type markedDTO struct {
	Marked int64 `json:"marked"`
}

// inboxPreferencesDTO serves the available kinds alongside the muted set, so
// the preference list shows exactly what this plane can emit and a new taxonomy
// entry needs no front-end change (spec §6).
type inboxPreferencesDTO struct {
	MutedKinds     []string `json:"muted_kinds"`
	AvailableKinds []string `json:"available_kinds"`
}

func toInboxItemDTO(it domain.InboxItem) inboxItemDTO {
	return inboxItemDTO{
		ID:        it.ID,
		ProjectID: it.ProjectID,
		Kind:      it.Kind,
		Severity:  string(it.Severity),
		Digest:    it.Digest,
		Title:     inbox.DisplayTitle(it),
		Body:      it.Body,
		Link:      it.Link,
		LinkLabel: it.LinkLabel,
		ReadAt:    it.ReadAt,
		CreatedAt: it.CreatedAt,
		UpdatedAt: it.UpdatedAt,
	}
}

// handleListInbox pages the caller's own items newest-first with a seek cursor:
// ?unread=true&limit=20&before=inb_… (spec §6).
func (a *API) handleListInbox(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeJSON(w, http.StatusOK, inboxPageDTO{Items: []inboxItemDTO{}})
		return
	}
	q := r.URL.Query()
	limit := 0 // 0 = the service's default
	if raw := q.Get("limit"); raw != "" {
		// ParseInt with an explicit bit size, not Atoi: the service narrows this
		// to int32 for the query's LIMIT, and Atoi's architecture-dependent int
		// silently wraps on that narrowing (CodeQL go/incorrect-integer-
		// conversion). Refusing an out-of-range number at the edge is also the
		// honest answer — the service clamp is a cap, not a parser.
		n, err := strconv.ParseInt(raw, 10, 32)
		if err != nil || n < 1 {
			writeError(w, http.StatusBadRequest, "limit must be a positive integer")
			return
		}
		limit = int(n)
	}
	page, err := a.deps.Inbox.List(r.Context(), user.ID, inbox.ListOptions{
		UnreadOnly: q.Get("unread") == "true",
		Limit:      limit,
		Before:     q.Get("before"),
	})
	if err != nil {
		a.writeInboxError(w, "listing inbox items", err)
		return
	}
	out := make([]inboxItemDTO, 0, len(page.Items))
	for _, it := range page.Items {
		out = append(out, toInboxItemDTO(it))
	}
	writeJSON(w, http.StatusOK, inboxPageDTO{Items: out, NextBefore: page.NextBefore})
}

// handleInboxUnreadCount is the bell's number — the other query every panel
// load runs.
func (a *API) handleInboxUnreadCount(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeJSON(w, http.StatusOK, unreadCountDTO{})
		return
	}
	n, err := a.deps.Inbox.UnreadCount(r.Context(), user.ID)
	if err != nil {
		a.writeInboxError(w, "counting unread inbox items", err)
		return
	}
	writeJSON(w, http.StatusOK, unreadCountDTO{Unread: n})
}

// handleMarkInboxItemRead is idempotent: marking an already-read item is 204
// and changes nothing. An item belonging to anyone else is 404 — the same
// answer as one that does not exist.
func (a *API) handleMarkInboxItemRead(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeError(w, http.StatusNotFound, "not found")
		return
	}
	if err := a.deps.Inbox.MarkRead(r.Context(), user.ID, r.PathValue("id")); err != nil {
		a.writeInboxError(w, "marking inbox item read", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleMarkAllInboxRead clears the count. The items stay listed — reading is
// not deleting.
func (a *API) handleMarkAllInboxRead(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeJSON(w, http.StatusOK, markedDTO{})
		return
	}
	n, err := a.deps.Inbox.MarkAllRead(r.Context(), user.ID)
	if err != nil {
		a.writeInboxError(w, "marking all inbox items read", err)
		return
	}
	writeJSON(w, http.StatusOK, markedDTO{Marked: n})
}

func (a *API) handleGetInboxPreferences(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeJSON(w, http.StatusOK, inboxPreferencesDTO{MutedKinds: []string{}, AvailableKinds: inbox.AvailableKinds()})
		return
	}
	p, err := a.deps.Inbox.Preferences(r.Context(), user.ID)
	if err != nil {
		a.writeInboxError(w, "getting inbox preferences", err)
		return
	}
	writeJSON(w, http.StatusOK, inboxPreferencesDTO{MutedKinds: p.MutedKinds, AvailableKinds: inbox.AvailableKinds()})
}

type putInboxPreferencesRequest struct {
	MutedKinds []string `json:"muted_kinds"`
}

// handlePutInboxPreferences replaces the whole muted set. Preferences are
// stored as MUTES, so an empty array means "everything on" and a kind added
// later is on by default for everyone (spec §2).
func (a *API) handlePutInboxPreferences(w http.ResponseWriter, r *http.Request) {
	user, _ := userFromContext(r.Context())
	if a.deps.Inbox == nil {
		writeError(w, http.StatusNotImplemented, "the notification inbox is not enabled")
		return
	}
	var req putInboxPreferencesRequest
	if err := decodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, "invalid request body")
		return
	}
	p, err := a.deps.Inbox.SetPreferences(r.Context(), user.ID, req.MutedKinds)
	if err != nil {
		a.writeInboxError(w, "setting inbox preferences", err)
		return
	}
	writeJSON(w, http.StatusOK, inboxPreferencesDTO{MutedKinds: p.MutedKinds, AvailableKinds: inbox.AvailableKinds()})
}

// writeInboxError maps service errors to status codes.
func (a *API) writeInboxError(w http.ResponseWriter, op string, err error) {
	var ve *inbox.ValidationError
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
