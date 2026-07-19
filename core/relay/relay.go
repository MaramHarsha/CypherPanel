// Package relay is the plane's transient image relay (ADR-008 path 2,
// builder-role-and-relay.md §3): one in-memory rendezvous per deployment
// where the builder's push stream meets the target's pull stream. Bytes flow
// through an io.Pipe — synchronous, so gRPC/HTTP-2 flow control is the
// end-to-end backpressure and plane memory per transfer stays a few chunks
// regardless of image size. Nothing is ever written to plane disk
// (threat-model §5.9 stays true for image blobs).
package relay

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrBusy is returned when a side attaches to a session that already has that
// side — a redelivered work item racing its still-running predecessor. The
// caller backs off and retries (NAK); the active transfer is not disturbed.
var ErrBusy = errors.New("relay: transfer already in progress for this deployment")

// ErrPeerTimeout is returned when the other side never arrives within the
// rendezvous window. The waiting side's work item redelivers and retries with
// a fresh session (builder-role-and-relay.md §6).
var ErrPeerTimeout = errors.New("relay: peer did not arrive in time")

// DefaultRendezvousWait bounds how long one side waits for its peer.
const DefaultRendezvousWait = 3 * time.Minute

// Relay holds the active sessions. Construct with New.
type Relay struct {
	wait time.Duration

	mu       sync.Mutex
	sessions map[string]*session
}

type session struct {
	pr *io.PipeReader
	pw *io.PipeWriter
	// pusher/puller close when that side attaches — the rendezvous signal.
	pusher chan struct{}
	puller chan struct{}
}

// New builds a Relay. wait <= 0 uses DefaultRendezvousWait.
func New(wait time.Duration) *Relay {
	if wait <= 0 {
		wait = DefaultRendezvousWait
	}
	return &Relay{wait: wait, sessions: make(map[string]*session)}
}

func (r *Relay) get(deploymentID string) *session {
	r.mu.Lock()
	defer r.mu.Unlock()
	s, ok := r.sessions[deploymentID]
	if !ok {
		pr, pw := io.Pipe()
		s = &session{pr: pr, pw: pw, pusher: make(chan struct{}), puller: make(chan struct{})}
		r.sessions[deploymentID] = s
	}
	return s
}

// attach claims one side of the session (closing its arrival channel) and
// waits for the peer's. A second claim of the same side gets ErrBusy.
func (r *Relay) attach(ctx context.Context, deploymentID string, mine, peer func(*session) chan struct{}) (*session, error) {
	s := r.get(deploymentID)
	r.mu.Lock()
	select {
	case <-mine(s):
		r.mu.Unlock()
		return nil, ErrBusy
	default:
		close(mine(s))
	}
	r.mu.Unlock()

	select {
	case <-peer(s):
		return s, nil
	case <-ctx.Done():
		r.Drop(deploymentID, ctx.Err())
		return nil, ctx.Err()
	case <-time.After(r.wait):
		r.Drop(deploymentID, ErrPeerTimeout)
		return nil, ErrPeerTimeout
	}
}

// Push claims the pusher side, waits for the puller, and returns the write
// end. The caller streams the image tar into it and Closes it on success
// (EOF to the puller) or CloseWithErrors it on failure.
func (r *Relay) Push(ctx context.Context, deploymentID string) (io.WriteCloser, error) {
	s, err := r.attach(ctx, deploymentID,
		func(s *session) chan struct{} { return s.pusher },
		func(s *session) chan struct{} { return s.puller })
	if err != nil {
		return nil, err
	}
	return s.pw, nil
}

// Pull claims the puller side, waits for the pusher, and returns the read
// end. The caller must Drop the session when its stream ends (either way).
func (r *Relay) Pull(ctx context.Context, deploymentID string) (io.Reader, error) {
	s, err := r.attach(ctx, deploymentID,
		func(s *session) chan struct{} { return s.puller },
		func(s *session) chan struct{} { return s.pusher })
	if err != nil {
		return nil, err
	}
	return s.pr, nil
}

// Drop tears a session down and forgets it, so any retry forms a fresh
// rendezvous. Only the write end is closed: that delivers cause (nil means a
// clean end-of-stream) to a reader and unblocks a stuck writer — closing the
// read end too would mask the cause behind io.ErrClosedPipe for both sides.
// Idempotent; a Drop for an unknown deployment is a no-op.
func (r *Relay) Drop(deploymentID string, cause error) {
	r.mu.Lock()
	s, ok := r.sessions[deploymentID]
	delete(r.sessions, deploymentID)
	r.mu.Unlock()
	if !ok {
		return
	}
	_ = s.pw.CloseWithError(cause)
}
