package domain

import "time"

// The control plane's own disaster recovery (plane-disaster-recovery.md §9).

// Recipient modes: where the public key came from.
const (
	// RecipientGenerated: the panel minted the pair and showed the private half
	// exactly once.
	RecipientGenerated = "generated"
	// RecipientProvided: the operator supplied a public key they already hold
	// the private half of — an age key from their own password manager, or a
	// team's shared recovery key.
	RecipientProvided = "provided"
)

// PlaneDRConfig is the singleton. Its EXISTENCE is the armed/disarmed answer:
// disarming deletes the row rather than flipping a flag, so there is never a
// stale configuration sitting beside a boolean.
type PlaneDRConfig struct {
	TargetID       string
	PathPrefix     string
	Schedule       string
	RetentionCount int
	// Recipient is the PUBLIC half. The plane can only ever write.
	Recipient     string
	RecipientMode string
	// RecipientVerifiedAt nil means armed but NOT PROVEN: the panel has never
	// watched anyone decrypt with the matching key, so it must not imply the
	// operator still has it. That distinction is the difference between a
	// recovery plan and the belief in one.
	RecipientVerifiedAt *time.Time
	LastRunAt           *time.Time
	LastStatus          string
	LastDetail          string
	CreatedAt           time.Time
	UpdatedAt           time.Time
}

// PlaneSnapshot is one encrypted archive of the whole control-plane database.
//
// Distinct from a BackupRecord, which is one dump of one managed database, and
// from the local upgrade snapshot, which never leaves the panel's own disk.
type PlaneSnapshot struct {
	ID            string
	ObjectKey     string
	PanelVersion  string
	SchemaVersion int64
	SizeBytes     int64
	SHA256        string
	RowCount      int64
	Recipient     string
	Status        string
	Detail        string
	StartedAt     time.Time
	FinishedAt    *time.Time
	PrunedAt      *time.Time
}
