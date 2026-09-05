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
	// A restore is its own record rather than a second kind of backup record:
	// it is an operation with steps and an outcome, and the design's blocking
	// popup needs to address one (managed-databases.md §"Restoring").
	PrefixDatabaseRestore = "rst"

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

	// DNS automation (dns-automation.md §2). A Zone is cached from the provider;
	// a Record is one the panel created and therefore owns.
	PrefixDNSZone   = "dnz"
	PrefixDNSRecord = "dnr"

	// V1.x: deploy protection (docs/features/deploy-protection.md §2). Only
	// two of the feature's four tables mint an id: an EnvironmentProtection is
	// keyed by its environment and a DeployApproval by its deployment, which
	// is what makes "one policy per environment" and "one gate decision per
	// deployment" invariants the database enforces rather than rules a service
	// has to remember.
	PrefixFreezeWindow = "fzw"
	PrefixBreakGlass   = "bg"

	// V1.x: the audit log (docs/features/audit-log.md §2). One id per recorded
	// action. Audit ids are never reused and never renumbered — an id in a
	// support conversation must keep naming the same event forever.
	PrefixAuditEvent = "aud"

	// V1.x: team invitations and access requests
	// (docs/features/invitations-and-access-requests.md §2). An invite id is
	// also the PUBLIC half of its wire token (`inv_….<secret>`), which is what
	// lets the lookup be an indexed primary-key read while only the secret's
	// hash is stored.
	PrefixTeamInvite    = "inv"
	PrefixAccessRequest = "acr"
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
