package events

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

func newEventID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

// Handler reacts to an event in-process. Handlers run in their own goroutine,
// must not block, and must not assume ordering — they are reactions, not steps.
type Handler func(ctx context.Context, e Event)

// Bus publishes domain events to JetStream and fans them out to in-process
// subscribers.
type Bus struct {
	js   jetstream.JetStream
	mu   sync.RWMutex
	subs map[string][]Handler // exact subject -> handlers; "" = all events
}

// NewBus connects JetStream and ensures the EVENTS stream exists. Retention is
// Limits (time/size based) — events are facts retained for many consumers, not
// a work queue that deletes on first ack.
func NewBus(ctx context.Context, nc *nats.Conn) (*Bus, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("events: jetstream context: %w", err)
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "CypherPanel domain events",
		Subjects:    []string{SubjectWildcard},
		Retention:   jetstream.LimitsPolicy,
		Storage:     jetstream.FileStorage,
		MaxAge:      14 * 24 * time.Hour,
		Duplicates:  5 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("events: ensuring stream %s: %w", StreamName, err)
	}
	return &Bus{js: js, subs: make(map[string][]Handler)}, nil
}

// Subscribe registers an in-process handler for an exact subject, or for all
// events when subject is "" or SubjectWildcard.
func (b *Bus) Subscribe(subject string, h Handler) {
	key := subject
	if subject == SubjectWildcard {
		key = ""
	}
	b.mu.Lock()
	b.subs[key] = append(b.subs[key], h)
	b.mu.Unlock()
}

// Publish records a domain event: durably to JetStream (for external/replica
// consumers) and to in-process subscribers. A publish failure is logged, not
// returned — emitting an event must never fail the operation that caused it
// (the fact already happened).
func (b *Bus) Publish(ctx context.Context, subject, aggregate, aggregateID string, data any) {
	blob, err := json.Marshal(data)
	if err != nil {
		slog.Error("events: encoding data", "subject", subject, "error", err)
		return
	}
	e := Event{
		ID:          newEventID(),
		Subject:     subject,
		Aggregate:   aggregate,
		AggregateID: aggregateID,
		OccurredAt:  time.Now().UTC(),
		Data:        blob,
	}

	if payload, err := e.encode(); err == nil {
		if _, err := b.js.Publish(ctx, subject, payload, jetstream.WithMsgID(e.ID)); err != nil {
			slog.Error("events: publishing to jetstream", "subject", subject, "error", err)
		}
	}

	b.dispatchLocal(e)
}

func (b *Bus) dispatchLocal(e Event) {
	b.mu.RLock()
	handlers := append(append([]Handler{}, b.subs[e.Subject]...), b.subs[""]...)
	b.mu.RUnlock()

	for _, h := range handlers {
		go func(h Handler) {
			defer func() {
				if r := recover(); r != nil {
					slog.Error("events: subscriber panicked", "subject", e.Subject, "recover", r)
				}
			}()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			h(ctx, e)
		}(h)
	}
}
