package jobs

import (
	"context"
	"fmt"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Publisher is CypherCore's side of the pipeline.
type Publisher struct {
	js jetstream.JetStream
}

// NewPublisher connects the JetStream context and ensures the TASKS stream
// exists (idempotent — safe across multiple Core replicas).
func NewPublisher(ctx context.Context, nc *nats.Conn) (*Publisher, error) {
	js, err := jetstream.New(nc)
	if err != nil {
		return nil, fmt.Errorf("jobs: jetstream context: %w", err)
	}
	_, err = js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:        StreamName,
		Description: "CypherPanel agent tasks",
		Subjects:    []string{subjectPrefix + ">"},
		Retention:   jetstream.WorkQueuePolicy,
		Storage:     jetstream.FileStorage,
		// Task IDs double as dedup keys within this window (idempotent publish).
		Duplicates: 10 * time.Minute,
	})
	if err != nil {
		return nil, fmt.Errorf("jobs: ensuring stream %s: %w", StreamName, err)
	}
	return &Publisher{js: js}, nil
}

// Publish enqueues a task for its server. Publishing the same task ID twice
// within the dedup window is a no-op, so "create DB row, then publish" can be
// retried safely.
func (p *Publisher) Publish(ctx context.Context, t Task) error {
	data, err := t.Encode()
	if err != nil {
		return err
	}
	_, err = p.js.Publish(ctx, Subject(t.ServerID), data, jetstream.WithMsgID(t.ID))
	if err != nil {
		return fmt.Errorf("jobs: publishing task %s: %w", t.ID, err)
	}
	return nil
}
