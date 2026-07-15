// Package cron manages a hosted account's crontab over Core↔Agent NATS
// request-reply. Jobs run as the account's own Linux user (the agent uses
// `crontab -u <user>`), so scheduled commands inherit the account's isolation.
package cron

// Op is a crontab operation.
type Op string

const (
	OpGet Op = "get"
	OpSet Op = "set"
)

// Request is a crontab read or write for one account.
type Request struct {
	Op       Op     `json:"op"`
	Username string `json:"username"`          // account system user
	Content  string `json:"content,omitempty"` // full crontab body for set
}

// Response carries the crontab body or an error.
type Response struct {
	Error   string `json:"error,omitempty"`
	Content string `json:"content,omitempty"`
}

// Subject is the NATS request subject a server's agent listens on for cron ops.
func Subject(serverID string) string { return "cron.server." + serverID }
