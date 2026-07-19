package worker

import (
	"context"
	"fmt"
	"io"
	"time"

	"github.com/nats-io/nats.go"

	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// natsBus is the production Bus: the agent's outbound NATS connection plus the
// JetStream pull subscription bound to the plane-created work consumer. It is
// thin plumbing over nats.go — the dispatch logic it feeds lives in worker.go
// and is tested against a fake Bus, so this file needs no server to exercise.
type natsBus struct {
	nc  *nats.Conn
	sub *nats.Subscription
}

// fetchWait bounds one FetchWork call so the run loop can check for
// cancellation between fetches.
const fetchWait = 2 * time.Second

// NewNATSBus binds to the durable work consumer the control plane created for
// this server and returns a Bus over the connection.
func NewNATSBus(nc *nats.Conn, serverID string) (Bus, error) {
	js, err := nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("worker: getting jetstream context: %w", err)
	}
	consumer := subjects.WorkConsumer(serverID)
	sub, err := js.PullSubscribe(subjects.WorkForServer(serverID), consumer, nats.Bind("WORK", consumer))
	if err != nil {
		return nil, fmt.Errorf("worker: binding to work consumer %s: %w", consumer, err)
	}
	return &natsBus{nc: nc, sub: sub}, nil
}

func (b *natsBus) Request(ctx context.Context, subject string, data []byte) ([]byte, error) {
	msg, err := b.nc.RequestWithContext(ctx, subject, data)
	if err != nil {
		return nil, err
	}
	return msg.Data, nil
}

func (b *natsBus) Publish(subject string, data []byte) error {
	return b.nc.Publish(subject, data)
}

func (b *natsBus) FetchWork(ctx context.Context) (Message, error) {
	// A pull Fetch takes a deadline OR a context, never both (nats.go rejects
	// setting both). MaxWait bounds the call so the Run loop can re-check
	// cancellation between fetches; ctx cancellation is honored there, within
	// one fetchWait window.
	select {
	case <-ctx.Done():
		return nil, context.Canceled
	default:
	}
	msgs, err := b.sub.Fetch(1, nats.MaxWait(fetchWait))
	if err != nil {
		if err == nats.ErrTimeout {
			return nil, ErrNoWork
		}
		return nil, err
	}
	if len(msgs) == 0 {
		return nil, ErrNoWork
	}
	return natsMessage{msgs[0]}, nil
}

// RelayPush streams r to the deployment's relay subject in 1 MiB chunks,
// terminated by an empty sentinel message (builder-role-and-relay.md §3).
func (b *natsBus) RelayPush(ctx context.Context, deploymentID string, r io.Reader) error {
	subj := subjects.Relay(deploymentID)
	buf := make([]byte, 1<<20)
	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("worker: relay push canceled: %w", err)
		}
		n, err := r.Read(buf)
		if n > 0 {
			if perr := b.nc.Publish(subj, buf[:n]); perr != nil {
				return fmt.Errorf("worker: publishing relay chunk for %s: %w", deploymentID, perr)
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("worker: reading image stream for %s: %w", deploymentID, err)
		}
	}
	if err := b.nc.Publish(subj, nil); err != nil { // end-of-stream sentinel
		return fmt.Errorf("worker: publishing relay sentinel for %s: %w", deploymentID, err)
	}
	// Publish is asynchronous; flush so transport errors surface before the
	// upload is reported successful.
	if err := b.nc.FlushWithContext(ctx); err != nil {
		return fmt.Errorf("worker: flushing relay upload for %s: %w", deploymentID, err)
	}
	return nil
}

// RelayPull consumes the deployment's relay stream from the beginning and
// exposes it as a reader; the returned ReadCloser yields chunks until the
// empty end-of-stream sentinel.
func (b *natsBus) RelayPull(ctx context.Context, deploymentID string) (io.ReadCloser, error) {
	subj := subjects.Relay(deploymentID)
	js, err := b.nc.JetStream()
	if err != nil {
		return nil, fmt.Errorf("worker: getting jetstream context: %w", err)
	}
	sub, err := js.SubscribeSync(subj, nats.DeliverAll())
	if err != nil {
		return nil, fmt.Errorf("worker: subscribing to relay for %s: %w", deploymentID, err)
	}

	pr, pw := io.Pipe()
	go func() {
		defer func() { _ = sub.Unsubscribe() }()
		for {
			msg, err := sub.NextMsgWithContext(ctx)
			if err != nil {
				pw.CloseWithError(err)
				return
			}
			_ = msg.Ack()
			if len(msg.Data) == 0 { // end-of-stream sentinel
				pw.Close()
				return
			}
			if _, err := pw.Write(msg.Data); err != nil {
				pw.CloseWithError(err)
				return
			}
		}
	}()
	return pr, nil
}

// natsMessage adapts a JetStream *nats.Msg to the worker's Message.
type natsMessage struct{ msg *nats.Msg }

func (m natsMessage) Subject() string                    { return m.msg.Subject }
func (m natsMessage) Data() []byte                       { return m.msg.Data }
func (m natsMessage) Ack() error                         { return m.msg.Ack() }
func (m natsMessage) Term() error                        { return m.msg.Term() }
func (m natsMessage) NakWithDelay(d time.Duration) error { return m.msg.NakWithDelay(d) }
func (m natsMessage) InProgress() error                  { return m.msg.InProgress() }

func (m natsMessage) NumDelivered() uint64 {
	meta, err := m.msg.Metadata()
	if err != nil {
		return 1 // treat unknown as first delivery — never over-eager to Term
	}
	return meta.NumDelivered
}
