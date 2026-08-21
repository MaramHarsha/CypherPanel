package domain

// The notification inbox (notification-inbox.md §2): the panel's own record of
// what happened to what you own. An item is a per-user row — tenancy is a
// column, not a resolver — and it is DENORMALISED on purpose: it stores the
// rendered statement about a moment, never a pointer resolved against current
// state (spec §2).

import "time"

// Inbox bounds (notification-inbox.md §5). The cap is per user and enforced by
// pruning on insert; the digest is what keeps a flood of successes from
// consuming it.
const (
	// InboxRetention is how many items one user's inbox keeps.
	InboxRetention = 200
	// InboxBodyMax caps a stored body, in bytes — the diagnostic-tail
	// discipline scheduled-tasks.md §6 already uses.
	InboxBodyMax = 2 << 10
)

// InboxItem is one persisted notification for one user. Severity is a
// NotifyLevel — the inbox subscribes to the existing event taxonomy and does
// not introduce a third status vocabulary (spec §3).
type InboxItem struct {
	ID        string
	UserID    string
	ProjectID string
	Kind      string
	Severity  NotifyLevel
	// Digest marks a rollup: one row per (user, project, kind, UTC day), whose
	// displayed line is composed from the counters at read time.
	Digest    bool
	Title     string
	Body      string
	Link      string
	LinkLabel string
	// CountOK / CountTotal are the digest's numerator and denominator. An
	// immediate item carries 1/1 and never shows them.
	CountOK    int
	CountTotal int
	// Sources are the focus ids already rolled into this row — the guard that
	// makes a redelivered observation a no-op (ENGINEERING rule 12).
	Sources   []string
	DedupeKey string
	// ReadAt is nil while the item is unread; that is what the bell counts.
	ReadAt    *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
}

// InboxPreferences is a user's notification filter, stored as MUTES: an absent
// row and an empty array both mean "everything on", so a kind added later is on
// by default for everyone (spec §2).
type InboxPreferences struct {
	UserID     string
	MutedKinds []string
	UpdatedAt  time.Time
}
