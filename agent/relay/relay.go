// Package relay is the agent side of the plane's transient image relay
// (builder-role-and-relay.md §3): a builder pushes its docker-save tar to the
// plane over the enrollment listener's mTLS channel, a target pulls it into
// its local daemon. The agent's client certificate is the caller identity the
// plane authorizes per-deployment.
package relay

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"

	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// chunkSize bounds one streamed message (spec §3: ≤1 MiB, so HTTP/2 flow
// control — not message size — is the memory bound end to end).
const chunkSize = 1 << 20

// Engine is what the relay needs from the local Docker daemon
// (consumer-defined; *engine.Client satisfies it).
type Engine interface {
	SaveImage(ctx context.Context, ref string) (io.ReadCloser, error)
	LoadImage(ctx context.Context, tar io.Reader) error
	HasImage(ctx context.Context, ref string) (bool, error)
}

// Client relays images between the local daemon and the plane.
type Client struct {
	cc     *grpc.ClientConn
	engine Engine
	log    *slog.Logger
}

// New wires a relay client against the plane's enrollment listener at addr
// (host:port), authenticating with the agent's mTLS identity. The connection
// is lazy: nothing is dialed until a transfer runs.
func New(addr string, id *identity.Identity, eng Engine, log *slog.Logger) (*Client, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, fmt.Errorf("relay: invalid plane address %q: %w", addr, err)
	}
	tlsCfg, err := pki.ClientTLSConfig(id.CertPEM, id.KeyPEM, id.CACertPEM, host)
	if err != nil {
		return nil, err
	}
	cc, err := grpc.NewClient(addr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("relay: dialing plane: %w", err)
	}
	return &Client{cc: cc, engine: eng, log: log}, nil
}

// PushImage streams the local image's docker-save tar to the plane's relay
// for one deployment. A plane that no longer needs the transfer answers OK
// early — that is success, not an error (spec §3).
func (c *Client) PushImage(ctx context.Context, deploymentID, image string) error {
	tar, err := c.engine.SaveImage(ctx, image)
	if err != nil {
		return fmt.Errorf("relay: saving %s: %w", image, err)
	}
	defer func() { _ = tar.Close() }()

	stream, err := agentv1.NewImageRelayServiceClient(c.cc).PushImage(ctx)
	if err != nil {
		return fmt.Errorf("relay: opening push stream: %w", err)
	}

	buf := make([]byte, chunkSize)
	first := true
	for {
		n, rerr := tar.Read(buf)
		if n > 0 {
			msg := &agentv1.PushImageRequest{Data: buf[:n]}
			if first {
				msg.DeploymentId = deploymentID
				first = false
			}
			if serr := stream.Send(msg); serr != nil {
				// The server closed the stream: either "relay not needed"
				// (OK response) or a real failure — CloseAndRecv tells which.
				if _, cerr := stream.CloseAndRecv(); cerr != nil {
					return fmt.Errorf("relay: pushing %s: %w", deploymentID, cerr)
				}
				return nil
			}
		}
		if errors.Is(rerr, io.EOF) {
			break
		}
		if rerr != nil {
			return fmt.Errorf("relay: reading image tar: %w", rerr)
		}
	}
	if first {
		// Zero-byte save cannot happen for a real image; refuse to relay
		// nothing rather than hand the plane an identity-less stream.
		return fmt.Errorf("relay: image %s produced an empty tar", image)
	}
	if _, err := stream.CloseAndRecv(); err != nil {
		return fmt.Errorf("relay: pushing %s: %w", deploymentID, err)
	}
	return nil
}

// PullImage obtains one deployment's image through the plane's relay and
// loads it into the local daemon. Idempotent: an image already present is
// immediate success (the redelivery/crash-recovery anchor, spec §6), and a
// load is only trusted if the daemon then reports the image present.
func (c *Client) PullImage(ctx context.Context, deploymentID, image string) error {
	if ok, err := c.engine.HasImage(ctx, image); err == nil && ok {
		return nil
	}

	stream, err := agentv1.NewImageRelayServiceClient(c.cc).PullImage(ctx, &agentv1.PullImageRequest{DeploymentId: deploymentID})
	if err != nil {
		return fmt.Errorf("relay: opening pull stream: %w", err)
	}
	if err := c.engine.LoadImage(ctx, &streamReader{stream: stream}); err != nil {
		return fmt.Errorf("relay: loading %s: %w", deploymentID, err)
	}
	ok, err := c.engine.HasImage(ctx, image)
	if err != nil {
		return fmt.Errorf("relay: verifying %s after load: %w", image, err)
	}
	if !ok {
		return fmt.Errorf("relay: %s not present after load (relayed tar did not contain it)", image)
	}
	return nil
}

// streamReader adapts the pull stream to io.Reader for the daemon's load.
type streamReader struct {
	stream grpc.ServerStreamingClient[agentv1.PullImageResponse]
	rest   []byte
}

func (r *streamReader) Read(p []byte) (int, error) {
	for len(r.rest) == 0 {
		msg, err := r.stream.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return 0, io.EOF
			}
			return 0, err
		}
		r.rest = msg.GetData()
	}
	n := copy(p, r.rest)
	r.rest = r.rest[n:]
	return n, nil
}
