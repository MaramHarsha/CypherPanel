package domain

import "time"

// Notification channel kinds (notifications.md §2). Stable wire/DB values.
const (
	NotifyChannelEmail    = "email"
	NotifyChannelDiscord  = "discord"
	NotifyChannelSlack    = "slack"
	NotifyChannelTelegram = "telegram"
)

// Notification event keys (notifications.md §3). A Notifier subscribes to a
// subset; these are the transitions the control plane already reaches a
// terminal decision on.
const (
	EventDeploySucceeded = "deploy.succeeded"
	EventDeployFailed    = "deploy.failed"
	EventBackupSucceeded = "backup.succeeded"
	EventBackupFailed    = "backup.failed"
)

// Notifier is a declarative "on these events, deliver to this channel" row,
// scoped to a Project. Channel config (SMTP creds, webhook URL, bot token) is
// sealed at rest and never returned in an API response (rule 20).
type Notifier struct {
	ID          string
	ProjectID   string
	Name        string
	Channel     string
	ConfigCT    []byte
	ConfigNonce []byte
	Events      []string
	Enabled     bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// NotifyLevel classifies an event for channel rendering (colour, prefix).
type NotifyLevel string

const (
	NotifyInfo  NotifyLevel = "info"
	NotifyError NotifyLevel = "error"
)

// NotifyEvent is the plane-domain value a channel sender renders. It carries no
// sealed material — only metadata already surfaced through the API
// (notifications.md §6).
type NotifyEvent struct {
	Type    string
	Level   NotifyLevel
	Title   string
	Body    string
	Project string
}
