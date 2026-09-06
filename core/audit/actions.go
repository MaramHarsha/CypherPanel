package audit

import "sort"

// The audit vocabulary (audit-log.md §3).
//
// An action is a dotted verb: the family before the dot, the past-tense event
// after it. The families are the nouns an operator reasons about, which is what
// makes `action=deploy` a useful filter on its own — the query matches a whole
// family by prefix, so three coarse choices in a filter menu cover the log
// without enumerating every verb.
//
// The set is CLOSED and validated: Record refuses an action outside it, because
// a typo'd verb would make its rows unfindable by the very filter that exists to
// find them. Adding a verb is a one-line change here plus a call site — never a
// migration, since the column is text.
//
// Failure is not a verb. `auth.login` with `outcome: failure` is the refused
// sign-in, so "everything that was refused" stays a single predicate over the
// whole vocabulary (canvas 13t).
const (
	// Sign-in and account credentials. These are the rows that make an account
	// takeover reconstructable, so the whole of core/auth's surface is here.
	ActionLogin                = "auth.login"
	ActionLogout               = "auth.logout"
	ActionPasswordChanged      = "auth.password_changed"
	ActionEmailChangeRequested = "auth.email_change_requested"
	ActionEmailChangeConfirmed = "auth.email_change_confirmed"
	ActionEmailChangeCancelled = "auth.email_change_cancelled"
	ActionTOTPEnabled          = "auth.totp_enabled"
	ActionTOTPDisabled         = "auth.totp_disabled"
	ActionSessionRevoked       = "auth.session_revoked"

	// Personal access tokens: the credential a leak turns into durable access.
	ActionTokenCreated = "token.created"
	ActionTokenRevoked = "token.revoked"

	// Panel accounts.
	ActionUserCreated     = "user.created"
	ActionUserRoleChanged = "user.role_changed"
	ActionUserDeleted     = "user.deleted"

	// Tenancy. A membership change is recorded against the TEAM, so a team's
	// timeline answers "who was let in, by whom, when".
	ActionTeamCreated           = "team.created"
	ActionTeamRenamed           = "team.renamed"
	ActionTeamDeleted           = "team.deleted"
	ActionTeamMemberAdded       = "team.member_added"
	ActionTeamMemberRoleChanged = "team.member_role_changed"
	ActionTeamMemberRemoved     = "team.member_removed"

	// Getting into a team from outside it
	// (invitations-and-access-requests.md §6). Recorded against the TEAM like
	// every other membership change, so one timeline answers "who was let in,
	// by whom, when" whichever door they came through. The detail carries the
	// address and the role; it never carries the invitation's token, and the
	// write path's secret-key stripping refuses a `token` key besides.
	ActionInviteCreated  = "invite.created"
	ActionInviteRevoked  = "invite.revoked"
	ActionInviteAccepted = "invite.accepted"

	ActionAccessRequested = "access.requested"
	ActionAccessGranted   = "access.granted"
	ActionAccessDenied    = "access.denied"

	// Servers. `server.enrolled` is the one the threat model names by hand
	// (§5.3, §8.1): a new server appearing must be a first-class, audited
	// event, not a log line.
	ActionServerCreated  = "server.created"
	ActionServerUpdated  = "server.updated"
	ActionServerDeleted  = "server.deleted"
	ActionServerEnrolled = "server.enrolled"

	// Deploy keys — the same class of credential as a token.
	ActionDeployKeyCreated = "deploy_key.created"
	ActionDeployKeyDeleted = "deploy_key.deleted"

	// Projects and environments. A transfer has its own verb because moving a
	// project between teams moves who can see everything inside it.
	ActionProjectCreated     = "project.created"
	ActionProjectUpdated     = "project.updated"
	ActionProjectTransferred = "project.transferred"
	ActionProjectDeleted     = "project.deleted"
	// A bulk read of a project's whole configuration, recorded before the
	// stream starts so an abandoned download is still on the record.
	ActionProjectExported = "project.exported"
	ActionEnvironmentCreated = "environment.created"
	ActionEnvironmentRenamed = "environment.renamed"
	ActionEnvironmentDeleted = "environment.deleted"
	// A template install creates several applications and databases in one
	// action, so it is recorded ONCE against the environment that received
	// them — six silent creates is not an answer to "where did these come
	// from?".
	ActionTemplateInstalled = "environment.template_installed"

	// Applications. An env-var change is recorded against the APPLICATION with
	// the KEY in the detail — never the value (§6) — so the application's own
	// timeline shows what was rewired.
	ActionApplicationCreated = "application.created"
	ActionApplicationUpdated = "application.updated"
	ActionApplicationDeleted = "application.deleted"
	// A restart is not a deploy — no revision, no build — but it is a
	// production action with a visible effect (deployment-control.md §3).
	ActionApplicationRestarted = "application.restarted"
	ActionEnvVarSet            = "application.env_var_set"
	ActionEnvVarRemoved        = "application.env_var_removed"

	// Compose Stacks (compose-stacks.md §7). The detail records THAT the file
	// changed, never its content: a compose file can carry an inline secret an
	// operator put there, and the audit log is not where it becomes permanent.
	ActionComposeStackCreated    = "compose_stack.created"
	ActionComposeStackUpdated    = "compose_stack.updated"
	ActionComposeStackDeleted    = "compose_stack.deleted"
	ActionComposeStackDeployed   = "compose_stack.deployed"
	ActionComposeStackRolledBack = "compose_stack.rolled_back"

	// Managed databases.
	ActionDatabaseCreated       = "database.created"
	ActionDatabaseUpdated       = "database.updated"
	ActionDatabaseDeleted       = "database.deleted"
	ActionDatabaseStopped       = "database.stopped"
	ActionDatabaseStarted       = "database.started"
	ActionDatabasePasswordReset = "database.password_reset"
	ActionDatabaseRestored      = "database.restore_requested"

	// The pipeline.
	ActionDeployStarted = "deploy.started"
	ActionRollback      = "deploy.rolled_back"
	// The operator stopped waiting on a deploy (deployment-control.md §2).
	ActionDeployCancelled = "deploy.cancelled"

	// Deploy protection: the decisions the deploy-protection spec deferred to
	// this log (deploy-protection.md §10).
	ActionProtectionSet    = "protection.policy_set"
	ActionDeployApproved   = "protection.approved"
	ActionDeployRejected   = "protection.rejected"
	ActionBreakGlassOpened = "protection.break_glass_opened"

	// Backups.
	ActionBackupTargetCreated   = "backup_target.created"
	ActionBackupTargetUpdated   = "backup_target.updated"
	ActionBackupTargetDeleted   = "backup_target.deleted"
	ActionBackupScheduleCreated = "backup_schedule.created"
	ActionBackupScheduleUpdated = "backup_schedule.updated"
	ActionBackupScheduleDeleted = "backup_schedule.deleted"
	ActionBackupRunRequested    = "backup.run_requested"

	// Project shared variables — sealed values, so only key and scope are ever
	// recorded.
	ActionSharedVariableCreated = "shared_variable.created"
	ActionSharedVariableUpdated = "shared_variable.updated"
	ActionSharedVariableDeleted = "shared_variable.deleted"

	// The two outbound channels.
	ActionNotifierCreated      = "notifier.created"
	ActionNotifierUpdated      = "notifier.updated"
	ActionNotifierDeleted      = "notifier.deleted"
	ActionWebhookCreated       = "webhook_endpoint.created"
	ActionWebhookUpdated       = "webhook_endpoint.updated"
	ActionWebhookDeleted       = "webhook_endpoint.deleted"
	ActionWebhookSecretRotated = "webhook_endpoint.secret_rotated"

	// Container registry credentials. The token is never in a detail — only
	// whether it was rotated (registries.md §6).
	ActionRegistryCreated = "registry.created"
	ActionRegistryUpdated = "registry.updated"
	ActionRegistryDeleted = "registry.deleted"

	// Panel-wide settings. Each one changes how the whole panel behaves, and
	// none belongs to a team — these are the rows a panel admin reads.
	ActionPanelSetupCompleted = "panel.setup_completed"
	ActionPanelMailUpdated    = "panel.mail_updated"
	ActionPanelMailDeleted    = "panel.mail_deleted"
	ActionPanelDNSUpdated     = "panel.dns_updated"
	ActionPanelDNSDeleted     = "panel.dns_deleted"
	ActionPanelTLSUpdated     = "panel.tls_updated"
)

// Resource kinds — the glossary noun the action was performed on. A resource
// kind is not a table name: an env-var change names the APPLICATION, and a
// membership change names the TEAM, because that is the timeline an operator
// reads them from.
const (
	ResourceUser            = "user"
	ResourceSession         = "session"
	ResourceAPIToken        = "api_token"
	ResourceTeam            = "team"
	ResourceTeamInvite      = "team_invite"
	ResourceAccessRequest   = "access_request"
	ResourceServer          = "server"
	ResourceDeployKey       = "deploy_key"
	ResourceProject         = "project"
	ResourceEnvironment     = "environment"
	ResourceApplication     = "application"
	ResourceDatabase        = "database"
	ResourceDeployment      = "deployment"
	ResourceBackupTarget    = "backup_target"
	ResourceBackupSchedule  = "backup_schedule"
	ResourceSharedVariable  = "shared_variable"
	ResourceNotifier        = "notifier"
	ResourceWebhookEndpoint = "webhook_endpoint"
	ResourceRegistry        = "registry"
	ResourceComposeStack    = "compose_stack"
	ResourcePanel           = "panel"
)

// actions is the closed set Record validates against.
var actions = map[string]bool{
	ActionLogin: true, ActionLogout: true, ActionPasswordChanged: true,
	ActionEmailChangeRequested: true, ActionEmailChangeConfirmed: true,
	ActionEmailChangeCancelled: true, ActionTOTPEnabled: true,
	ActionTOTPDisabled: true, ActionSessionRevoked: true,

	ActionTokenCreated: true, ActionTokenRevoked: true,

	ActionUserCreated: true, ActionUserRoleChanged: true, ActionUserDeleted: true,

	ActionTeamCreated: true, ActionTeamRenamed: true, ActionTeamDeleted: true,
	ActionTeamMemberAdded: true, ActionTeamMemberRoleChanged: true,
	ActionTeamMemberRemoved: true,

	ActionInviteCreated: true, ActionInviteRevoked: true,
	ActionInviteAccepted: true, ActionAccessRequested: true,
	ActionAccessGranted: true, ActionAccessDenied: true,

	ActionServerCreated: true, ActionServerUpdated: true,
	ActionServerDeleted: true, ActionServerEnrolled: true,

	ActionDeployKeyCreated: true, ActionDeployKeyDeleted: true,

	ActionProjectCreated: true, ActionProjectUpdated: true,
	ActionProjectTransferred: true, ActionProjectDeleted: true,
	ActionEnvironmentCreated: true, ActionEnvironmentRenamed: true,
	ActionEnvironmentDeleted: true, ActionTemplateInstalled: true,

	ActionApplicationCreated: true, ActionApplicationUpdated: true,
	ActionApplicationDeleted: true, ActionEnvVarSet: true,
	ActionEnvVarRemoved: true, ActionApplicationRestarted: true,

	ActionDatabaseCreated: true, ActionDatabaseUpdated: true,
	ActionDatabaseDeleted: true, ActionDatabaseStopped: true,
	ActionDatabaseStarted: true, ActionDatabasePasswordReset: true,
	ActionDatabaseRestored: true,

	ActionDeployStarted: true, ActionRollback: true,
	ActionDeployCancelled: true,

	ActionProtectionSet: true, ActionDeployApproved: true,
	ActionDeployRejected: true, ActionBreakGlassOpened: true,

	ActionBackupTargetCreated: true, ActionBackupTargetUpdated: true,
	ActionBackupTargetDeleted: true, ActionBackupScheduleCreated: true,
	ActionBackupScheduleUpdated: true, ActionBackupScheduleDeleted: true,
	ActionBackupRunRequested: true,

	ActionSharedVariableCreated: true, ActionSharedVariableUpdated: true,
	ActionSharedVariableDeleted: true,

	ActionNotifierCreated: true, ActionNotifierUpdated: true,
	ActionNotifierDeleted: true, ActionWebhookCreated: true,
	ActionWebhookUpdated: true, ActionWebhookDeleted: true,
	ActionWebhookSecretRotated: true,

	ActionProjectExported: true,

	ActionRegistryCreated: true, ActionRegistryUpdated: true,
	ActionRegistryDeleted: true,

	ActionComposeStackCreated: true, ActionComposeStackUpdated: true,
	ActionComposeStackDeleted: true, ActionComposeStackDeployed: true,
	ActionComposeStackRolledBack: true,

	ActionPanelSetupCompleted: true, ActionPanelMailUpdated: true,
	ActionPanelMailDeleted: true, ActionPanelDNSUpdated: true,
	ActionPanelDNSDeleted: true, ActionPanelTLSUpdated: true,
}

// ValidAction reports whether action is in the closed vocabulary.
func ValidAction(action string) bool { return actions[action] }

// Actions returns the whole vocabulary, sorted — what a filter menu offers and
// what the spec's action table is checked against.
func Actions() []string {
	out := make([]string, 0, len(actions))
	for a := range actions {
		out = append(out, a)
	}
	sort.Strings(out)
	return out
}
