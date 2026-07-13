package jobs

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"
)

// Handler executes one task. It must be idempotent: JetStream redelivers
// unacknowledged messages, and the same task may arrive more than once.
// A returned error means "retry later" (nak); nil means done (ack).
type Handler func(ctx context.Context, t Task) error

// ResultReporter delivers the task outcome back to the control plane
// (implemented by the agent's gRPC client).
type ResultReporter func(ctx context.Context, t Task, taskErr error)

const maxDeliveries = 5

// Consume runs the agent's durable consumer for its own server subject until
// ctx is cancelled. After maxDeliveries failed attempts a task is terminated
// (dead-lettered) and reported as failed rather than retried forever.
func Consume(ctx context.Context, nc *nats.Conn, serverID string, handle Handler, report ResultReporter) error {
	js, err := jetstream.New(nc)
	if err != nil {
		return fmt.Errorf("jobs: jetstream context: %w", err)
	}

	// Durable name must be subject-stable; UUID hyphens are not allowed in
	// consumer names, so strip them.
	durable := "agent_" + strings.ReplaceAll(serverID, "-", "")
	cons, err := js.CreateOrUpdateConsumer(ctx, StreamName, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: Subject(serverID),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
		MaxDeliver:    maxDeliveries,
	})
	if err != nil {
		return fmt.Errorf("jobs: ensuring consumer %s: %w", durable, err)
	}

	iter, err := cons.Messages()
	if err != nil {
		return fmt.Errorf("jobs: opening message iterator: %w", err)
	}
	go func() {
		<-ctx.Done()
		iter.Stop()
	}()

	slog.Info("task consumer running", "subject", Subject(serverID), "durable", durable)
	for {
		msg, err := iter.Next()
		if err != nil {
			if ctx.Err() != nil {
				return nil // clean shutdown
			}
			return fmt.Errorf("jobs: message iterator: %w", err)
		}
		processMessage(ctx, msg, handle, report)
	}
}

func processMessage(ctx context.Context, msg jetstream.Msg, handle Handler, report ResultReporter) {
	task, err := Decode(msg.Data())
	if err != nil {
		// Malformed payloads can never succeed; drop them permanently.
		slog.Error("dropping malformed task", "error", err)
		_ = msg.Term()
		return
	}

	meta, _ := msg.Metadata()
	attempt := uint64(1)
	if meta != nil {
		attempt = meta.NumDelivered
	}

	taskCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
	taskErr := handle(taskCtx, task)
	cancel()

	switch {
	case taskErr == nil:
		slog.Info("task succeeded", "task_id", task.ID, "type", task.Type)
		report(ctx, task, nil)
		_ = msg.Ack()
	case IsPermanent(taskErr) || attempt >= maxDeliveries:
		slog.Error("task failed permanently", "task_id", task.ID, "type", task.Type, "attempts", attempt, "error", taskErr)
		report(ctx, task, taskErr)
		_ = msg.Term()
	default:
		slog.Warn("task failed; will be redelivered", "task_id", task.ID, "type", task.Type, "attempt", attempt, "error", taskErr)
		_ = msg.Nak()
	}
}
