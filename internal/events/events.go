// Package events is CypherCore's internal Event Bus (plan.md §12). Domain
// events are facts-that-happened, published to the NATS JetStream `events.>`
// subject tree for fan-out to many consumers (webhooks, plugins, analytics),
// AND dispatched to in-process subscribers so a single Core instance reacts
// locally without a NATS round-trip.
//
// This tree is kept strictly separate from agent-task subjects
// (`tasks.server.<id>`): tasks are work-to-do (one consumer, WorkQueue),
// events are facts (many consumers, retained).
package events

import (
	"encoding/json"
	"fmt"
	"time"
)

const (
	// StreamName holds all domain events; subjects are events.<aggregate>.<verb>.
	StreamName    = "EVENTS"
	SubjectPrefix = "events."
	// SubjectWildcard is the stream's subject filter and a subscribe-to-all pattern.
	SubjectWildcard = "events.>"
)

// Subjects — past-tense facts. Add by extending this list; never rename a
// shipped subject (consumers depend on them).
const (
	SubjectAccountCreated     = "events.account.created"
	SubjectAccountActivated   = "events.account.activated"
	SubjectAccountSuspended   = "events.account.suspended"
	SubjectAccountUnsuspended = "events.account.unsuspended"
	SubjectAccountTerminating = "events.account.terminating"
	SubjectAccountTerminated  = "events.account.terminated"
	SubjectAccountFailed      = "events.account.failed"
	SubjectAccountSSLIssued   = "events.account.ssl_issued"
	SubjectPackageCreated     = "events.package.created"
	SubjectPackageDeleted     = "events.package.deleted"
	SubjectServerRegistered   = "events.server.registered"
	SubjectResellerCreated    = "events.reseller.created"
)

// Event is the wire format on the bus. Data is a minimal, immutable snapshot
// of the aggregate — enough for a consumer to act without re-fetching, and
// never containing secrets.
type Event struct {
	ID          string          `json:"id"`
	Subject     string          `json:"subject"`
	Aggregate   string          `json:"aggregate"`
	AggregateID string          `json:"aggregate_id"`
	OccurredAt  time.Time       `json:"occurred_at"`
	Data        json.RawMessage `json:"data"`
}

func (e Event) encode() ([]byte, error) {
	b, err := json.Marshal(e)
	if err != nil {
		return nil, fmt.Errorf("events: encoding %s: %w", e.Subject, err)
	}
	return b, nil
}

// Decode parses an event off the wire (for JetStream/webhook consumers).
func Decode(data []byte) (Event, error) {
	var e Event
	if err := json.Unmarshal(data, &e); err != nil {
		return Event{}, fmt.Errorf("events: decoding: %w", err)
	}
	if e.Subject == "" {
		return Event{}, fmt.Errorf("events: event missing subject")
	}
	return e, nil
}
