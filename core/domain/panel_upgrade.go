package domain

import "time"

// PanelUpgrade is one guided upgrade of the control plane itself
// (panel-updates.md §6).
type PanelUpgrade struct {
	ID          string
	FromVersion string
	ToVersion   string
	Phase       string
	Detail      string
	// Actor is who asked. `external` marks a version change this panel did not
	// perform — a boot whose version differs from the last recorded one, which
	// is what a compose install's upgrades look like from in here.
	Actor      string
	Rollback   bool
	SnapshotID string
	StartedAt  time.Time
	FinishedAt *time.Time
	ExpiresAt  time.Time
}

// UpgradeActorExternal marks a version change the panel observed rather than
// performed.
const UpgradeActorExternal = "external"

// Active reports whether this upgrade still holds the read-only lock.
func (u PanelUpgrade) Active(now time.Time) bool {
	return u.FinishedAt == nil && now.Before(u.ExpiresAt)
}

// PanelSnapshot is a fallback dump of the panel's own database.
type PanelSnapshot struct {
	ID        string
	Version   string
	Path      string
	SizeBytes int64
	CreatedAt time.Time
	// ExpiresAt nil means keep forever.
	ExpiresAt *time.Time
	Pinned    bool
}
