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

// Panel-level inbox kinds (control-plane-hardening.md §3): events about the
// panel itself rather than about a project's resources. They are inbox kinds
// only — never a notifier or webhook subscription — because nothing emits
// them to a channel; the inbox is their one audience.
const (
	// InboxKindPanelUpdateAvailable: a newer cypherd release was seen by the
	// update check. Written once per version to owners.
	InboxKindPanelUpdateAvailable = "panel.update_available"

	// InboxKindServerDiskLow / InboxKindServerDiskRecovered: a server crossed
	// the disk threshold, or crossed back (disk-management.md §5).
	//
	// Panel-level rather than subscribable, and the reason is structural: a
	// Server belongs to no project, and a Notifier is scoped to one. There is
	// no project whose channel should receive this, and delivering it to every
	// project's would be worse than not delivering it. It goes to the people
	// who can act on a server — the panel's owners and admins.
	//
	// Channel delivery for it therefore waits on panel-level notifiers, which
	// do not exist; that is a real gap and it is named here rather than papered
	// over by attaching a server to an arbitrary project.
	InboxKindServerDiskLow       = "server.disk_low"
	InboxKindServerDiskRecovered = "server.disk_recovered"
)

// panelInboxKinds is the panel-level half of the inbox taxonomy.
var panelInboxKinds = []string{
	InboxKindPanelUpdateAvailable,
	InboxKindServerDiskLow,
	InboxKindServerDiskRecovered,
}

// Deploy-protection inbox kinds (deploy-protection.md §9). Like the panel-level
// kinds these are inbox kinds ONLY — never a notifier or an outbound-webhook
// subscription. notifications.md §3's taxonomy is fed by TERMINAL transitions,
// and a parked deploy has not finished; a decision on it is governance news for
// two named audiences, not an outcome to broadcast to a channel.
const (
	// InboxKindDeployAwaitingApproval: a deploy parked on this environment's
	// approval gate. Addressed to the members who could actually act on it —
	// those at or above the approval's required_role.
	InboxKindDeployAwaitingApproval = "deploy.awaiting_approval"
	// InboxKindDeployApproved / InboxKindDeployRejected: the decision, back to
	// the person who asked for the deploy and nobody else.
	InboxKindDeployApproved = "deploy.approved"
	InboxKindDeployRejected = "deploy.rejected"
)

// protectionInboxKinds is the deploy-protection half of the inbox taxonomy.
var protectionInboxKinds = []string{
	InboxKindDeployAwaitingApproval,
	InboxKindDeployApproved,
	InboxKindDeployRejected,
}

// Team-access inbox kinds (invitations-and-access-requests.md §6). Like the
// panel-level and deploy-protection kinds these are inbox kinds ONLY — never a
// notifier or outbound-webhook subscription: notifications.md §3's taxonomy is
// fed by terminal observed transitions of resources, and "who is allowed in
// this team" is governance news for named people, not an outcome to broadcast
// to a channel.
//
// They are also the inbox's first TEAM-scoped items: they belong to a team,
// not to a project and not to the panel (InboxItem.TeamID).
const (
	// InboxKindAccessRequested: a member asked for a higher rank. Addressed to
	// the team's owners, who are the only people who can decide it.
	InboxKindAccessRequested = "access.requested"
	// InboxKindAccessGranted / InboxKindAccessDenied: the decision, back to the
	// person who asked and nobody else.
	InboxKindAccessGranted = "access.granted"
	InboxKindAccessDenied  = "access.denied"
	// InboxKindInviteAccepted: someone joined on an invitation. Addressed to
	// the member who sent it.
	InboxKindInviteAccepted = "invite.accepted"
)

// accessInboxKinds is the team-access half of the inbox taxonomy.
var accessInboxKinds = []string{
	InboxKindAccessRequested,
	InboxKindAccessGranted,
	InboxKindAccessDenied,
	InboxKindInviteAccepted,
}

// InboxKinds returns every kind an inbox item may carry and a preference list
// may mute: the subscribable event taxonomy first, then the inbox-only kinds
// (panel-level, then deploy protection, then team access). A copy — callers
// may not mutate the taxonomy.
func InboxKinds() []string {
	out := EventTypes()
	out = append(out, panelInboxKinds...)
	out = append(out, protectionInboxKinds...)
	return append(out, accessInboxKinds...)
}

// ValidInboxKind reports whether key is an inbox kind (ValidEventType or one of
// the inbox-only kinds).
func ValidInboxKind(key string) bool {
	if ValidEventType(key) {
		return true
	}
	for _, k := range panelInboxKinds {
		if k == key {
			return true
		}
	}
	for _, k := range protectionInboxKinds {
		if k == key {
			return true
		}
	}
	for _, k := range accessInboxKinds {
		if k == key {
			return true
		}
	}
	return false
}

// InboxItem is one persisted notification for one user. Severity is a
// NotifyLevel — the inbox subscribes to the existing event taxonomy and does
// not introduce a third status vocabulary (spec §3).
type InboxItem struct {
	ID     string
	UserID string
	// ProjectID is empty for a panel-level kind (InboxKinds beyond
	// EventTypes): the item belongs to the panel, not to a project.
	ProjectID string
	// TeamID is set for the team-access kinds and empty for everything else: an
	// item is scoped to a project, to a team, or to the panel — never to two of
	// them (invitations-and-access-requests.md §6).
	TeamID   string
	Kind     string
	Severity NotifyLevel
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
