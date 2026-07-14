// Package jobs is the asynchronous task pipeline between CypherCore and
// CypherAgents, built on NATS JetStream (plan.md Section 3). Core publishes
// to a per-server subject; each agent runs a durable consumer for its own
// subject only. Tasks must be idempotent: JetStream redelivers on failure,
// and message deduplication uses the task ID (plan.md Section 8).
package jobs

import (
	"encoding/json"
	"errors"
	"fmt"
)

const (
	// StreamName holds all agent tasks; subjects are tasks.server.<uuid>.
	StreamName    = "TASKS"
	subjectPrefix = "tasks.server."
)

// Known task types. Every type must have an idempotent agent handler.
const (
	TypeNoop             = "noop"               // health/testing: always succeeds
	TypeSystemUserCreate = "system_user.create" // payload: SystemUserCreatePayload
	TypeSystemUserRemove = "system_user.remove" // payload: SystemUserRemovePayload
	TypeSiteProvision    = "site.provision"     // payload: SiteProvisionPayload
	TypeSiteDeprovision  = "site.deprovision"   // payload: SiteDeprovisionPayload
	TypeSSLIssue         = "ssl.issue"          // payload: SSLIssuePayload
)

// Task is the wire format published to JetStream.
type Task struct {
	ID       string          `json:"id"`
	ServerID string          `json:"server_id"`
	Type     string          `json:"type"`
	Payload  json.RawMessage `json:"payload"`
}

// SystemUserCreatePayload creates the dedicated Linux user for a hosted
// account (plan.md Section 7). HomeDir is optional; the agent derives it
// from its distro path layout when empty.
type SystemUserCreatePayload struct {
	Username string `json:"username"`
	HomeDir  string `json:"home_dir,omitempty"`
}

// SystemUserRemovePayload removes a hosted account's Linux user (and home
// directory) during account termination.
type SystemUserRemovePayload struct {
	Username string `json:"username"`
}

// SiteProvisionPayload sets up an account's web serving: web root + logs dirs,
// nginx vhost, and a dedicated PHP-FPM pool.
type SiteProvisionPayload struct {
	Username    string            `json:"username"`
	Domain      string            `json:"domain"`
	PHPVersion  string            `json:"php_version"`
	MemoryMB    int               `json:"memory_mb,omitempty"`    // package memory limit (0 = default)
	PHPSettings map[string]string `json:"php_settings,omitempty"` // validated php.ini overrides
}

// SiteDeprovisionPayload removes an account's web/PHP configs on termination.
type SiteDeprovisionPayload struct {
	Username   string `json:"username"`
	Domain     string `json:"domain"`
	PHPVersion string `json:"php_version"`
}

// SSLIssuePayload issues (or renews) a Let's Encrypt certificate for a domain
// and switches its vhost to HTTPS.
type SSLIssuePayload struct {
	Username string `json:"username"`
	Domain   string `json:"domain"`
	Email    string `json:"email"`
}

// Result metadata keys reported back with ssl.issue.
const (
	MetaSSLNotAfter = "ssl_not_after" // RFC3339 certificate expiry
)

// ValidType reports whether the task type is known to this build. Core
// rejects unknown types at the API boundary rather than letting them fail
// on every agent redelivery.
func ValidType(t string) bool {
	switch t {
	case TypeNoop, TypeSystemUserCreate, TypeSystemUserRemove, TypeSiteProvision, TypeSiteDeprovision, TypeSSLIssue:
		return true
	}
	return false
}

// Subject returns the NATS subject for a server's task queue.
func Subject(serverID string) string {
	return subjectPrefix + serverID
}

func (t Task) Encode() ([]byte, error) {
	b, err := json.Marshal(t)
	if err != nil {
		return nil, fmt.Errorf("jobs: encoding task %s: %w", t.ID, err)
	}
	return b, nil
}

// Permanent marks a handler error as non-retryable: the task is
// dead-lettered and reported failed immediately instead of redelivered.
func Permanent(err error) error { return permanentError{err} }

// IsPermanent reports whether err (or anything it wraps) was marked Permanent.
func IsPermanent(err error) bool {
	var pe permanentError
	return errors.As(err, &pe)
}

type permanentError struct{ err error }

func (e permanentError) Error() string { return e.err.Error() }
func (e permanentError) Unwrap() error { return e.err }

func Decode(data []byte) (Task, error) {
	var t Task
	if err := json.Unmarshal(data, &t); err != nil {
		return Task{}, fmt.Errorf("jobs: decoding task: %w", err)
	}
	if t.ID == "" || t.Type == "" {
		return Task{}, fmt.Errorf("jobs: task missing id or type")
	}
	return t, nil
}
