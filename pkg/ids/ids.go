// Package ids generates the identifiers and secrets used across CypherPanel:
// prefixed, URL-safe resource IDs and high-entropy opaque tokens.
//
// All randomness comes from crypto/rand via the standard library's rand.Text,
// which draws from the OS CSPRNG and does not surface an error (Go 1.24+).
package ids

import (
	"crypto/rand"
	"strings"
)

// Resource ID prefixes. These are stable wire/DB values — never renumber or
// repurpose an existing prefix (it would alias historical identifiers).
const (
	PrefixServer      = "srv"
	PrefixUser        = "usr"
	PrefixJoinToken   = "jt"
	PrefixAPIToken    = "tok"
	PrefixSession     = "ses"
	PrefixProject     = "prj"
	PrefixEnvironment = "env"
	PrefixApplication = "app"
	PrefixPreview     = "prv"
	PrefixRevision    = "rev"
	PrefixDeployment  = "dep"
	PrefixWebhook     = "wh"
	PrefixDeployKey   = "dk"

	// Phase 3: Managed Databases (docs/features/managed-databases.md).
	PrefixDatabase         = "db"
	PrefixDatabaseRevision = "dbr"
	PrefixBackupTarget     = "bt"
	PrefixDatabaseBackup   = "bak"
	PrefixBackupRecord     = "br"

	// Phase 3: Notifications (docs/features/notifications.md).
	PrefixNotifier = "ntf"

	// Phase 3: Scheduled tasks (docs/features/scheduled-tasks.md).
	PrefixScheduledTask = "sch"
	PrefixTaskRun       = "str"

	// Phase 3: Teams (docs/features/teams-and-roles.md).
	PrefixTeam = "tm"

	// V1: Two-factor auth recovery codes (docs/features/two-factor-auth.md).
	PrefixRecoveryCode = "rc"

	// Phase 4: Outbound webhooks (docs/features/outbound-webhooks.md). A
	// delivery id is also the X-CypherPanel-Delivery header value receivers
	// dedupe on; attempts carry no prefix — they key on (delivery_id, attempt).
	PrefixWebhookEndpoint = "whe"
	PrefixWebhookDelivery = "whd"

	// Phase 4: the notification inbox (docs/features/notification-inbox.md).
	// One id per RECIPIENT, not per event: an item is a per-user row, and two
	// members of a team hold two rows for the same observed outcome.
	PrefixInboxItem = "inb"

	// Phase 4: project shared variables (docs/features/shared-variables.md).
	// One id per variable regardless of scope — project scope and environment
	// scope are the same row shape, which is what lets a value be promoted
	// between them without touching any referencing application (§3).
	PrefixSharedVariable = "sv"

	// V1: Panel mail and email changes (docs/features/panel-mail.md).
	PrefixEmailChange = "ec"
)

// New returns a prefixed, URL-safe, collision-resistant identifier such as
// "srv_k7q2v9v3m8...". The random component carries ~130 bits of entropy.
func New(prefix string) string {
	return prefix + "_" + strings.ToLower(rand.Text())
}

// Secret returns a fresh high-entropy opaque secret (~130 bits) suitable for a
// join token or similar bearer credential. Unlike New it carries no prefix and
// preserves the full base32 alphabet, so it is meant to be compared, never
// parsed. Store only a hash of it (see core/store); compare in constant time.
func Secret() string {
	return rand.Text()
}
