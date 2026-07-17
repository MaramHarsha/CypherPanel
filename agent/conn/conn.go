// Package conn owns the agent's connections to the control plane: the one-time
// gRPC enrollment handshake and the persistent mTLS NATS connection (ADR-002).
// Both verify the plane against the pinned CA; the persistent connection is
// outbound-only and reconnects on its own (threat-model §5.4).
package conn

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"

	"github.com/MaramHarsha/cypherpanel/agent/identity"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// Enroll performs the one-time enrollment handshake and returns the agent's new
// identity. The private key is generated locally and never transmitted; only a
// CSR is sent (threat-model §5.1).
func Enroll(ctx context.Context, planeAddr, token string, caPEM []byte, hostname, version string) (*identity.Identity, error) {
	keyPEM, csrPEM, err := pki.GenerateAgentKey(hostname)
	if err != nil {
		return nil, err
	}
	serverName, err := hostOf(planeAddr)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := pki.BootstrapClientTLSConfig(caPEM, serverName)
	if err != nil {
		return nil, err
	}
	cc, err := grpc.NewClient(planeAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("conn: dialing plane: %w", err)
	}
	defer func() { _ = cc.Close() }()

	resp, err := agentv1.NewEnrollmentServiceClient(cc).Enroll(ctx, &agentv1.EnrollRequest{
		JoinToken:    token,
		CsrPem:       csrPEM,
		Hostname:     hostname,
		AgentVersion: version,
	})
	if err != nil {
		return nil, fmt.Errorf("conn: enrolling: %w", err)
	}
	return &identity.Identity{
		ServerID:  resp.GetServerId(),
		NATSURL:   resp.GetNatsUrl(),
		CertPEM:   resp.GetCertificatePem(),
		KeyPEM:    keyPEM,
		CACertPEM: resp.GetCaPem(),
	}, nil
}

// ConnectBus opens the persistent mTLS NATS connection using a saved identity.
// It reconnects indefinitely so a control-plane restart is transparent to the
// agent (Phase 1 acceptance: kill the plane, the agent reconverges).
func ConnectBus(id *identity.Identity, log *slog.Logger) (*nats.Conn, error) {
	serverName, err := hostOfURL(id.NATSURL)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := pki.ClientTLSConfig(id.CertPEM, id.KeyPEM, id.CACertPEM, serverName)
	if err != nil {
		return nil, err
	}
	nc, err := nats.Connect(id.NATSURL,
		nats.Secure(tlsCfg),
		nats.Name("cypher-agent/"+id.ServerID),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2*time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("bus disconnected, will retry", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Info("bus reconnected")
		}),
	)
	if err != nil {
		return nil, fmt.Errorf("conn: connecting to bus: %w", err)
	}
	return nc, nil
}

func hostOf(addr string) (string, error) {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return "", fmt.Errorf("conn: parsing plane addr %q: %w", addr, err)
	}
	return host, nil
}

func hostOfURL(raw string) (string, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("conn: parsing nats url %q: %w", raw, err)
	}
	if u.Hostname() == "" {
		return "", fmt.Errorf("conn: nats url %q has no host", raw)
	}
	return u.Hostname(), nil
}
