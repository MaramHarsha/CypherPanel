// Package conn owns the agent's connections to the control plane: the one-time
// gRPC enrollment handshake and the persistent mTLS NATS connection (ADR-002).
// Both verify the plane against the pinned CA; the persistent connection is
// outbound-only and reconnects on its own (threat-model §5.4).
package conn

import (
	"context"
	"crypto/tls"
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
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
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
		PlaneAddr: planeAddr,
		CertPEM:   resp.GetCertificatePem(),
		KeyPEM:    keyPEM,
		CACertPEM: resp.GetCaPem(),
	}, nil
}

// ConnectBus opens the persistent mTLS NATS connection using the agent's
// current identity. It reconnects indefinitely so a control-plane restart is
// transparent to the agent (Phase 1 acceptance: kill the plane, the agent
// reconverges).
//
// The client certificate is resolved from the Keeper at every handshake rather
// than fixed at dial time, so a certificate renewed mid-life is picked up by
// the next reconnect on its own — no restart, and no desired state dropped
// (agent-identity-and-tls.md §3).
func ConnectBus(k *identity.Keeper, log *slog.Logger) (*nats.Conn, error) {
	serverName, err := hostOfURL(k.NATSURL())
	if err != nil {
		return nil, err
	}
	tlsCfg, err := pki.ClientTLSConfigFunc(k.Certificate, k.CACertPEM(), serverName)
	if err != nil {
		return nil, err
	}
	nc, err := nats.Connect(k.NATSURL(), busOptions(k.ServerID(), tlsCfg, log)...)
	if err != nil {
		return nil, fmt.Errorf("conn: connecting to bus: %w", err)
	}
	return nc, nil
}

// Renewer requests re-signed certificates from the plane over the mTLS channel
// the agent already holds (agent-identity-and-tls.md §3). It satisfies
// identity.Signer.
//
// The connection is lazy and long-lived: gRPC dials on first use and presents
// whatever certificate the Keeper holds at that moment, which is what lets the
// renewer keep working across the very rotations it performs.
type Renewer struct {
	cc      *grpc.ClientConn
	version string
}

// NewRenewer wires a renewal client against the plane's enrollment listener at
// planeAddr (host:port) — the same endpoint the agent enrolled through.
func NewRenewer(planeAddr string, k *identity.Keeper, version string) (*Renewer, error) {
	serverName, err := hostOf(planeAddr)
	if err != nil {
		return nil, err
	}
	tlsCfg, err := pki.ClientTLSConfigFunc(k.Certificate, k.CACertPEM(), serverName)
	if err != nil {
		return nil, err
	}
	cc, err := grpc.NewClient(planeAddr, grpc.WithTransportCredentials(credentials.NewTLS(tlsCfg)))
	if err != nil {
		return nil, fmt.Errorf("conn: dialing plane for renewal: %w", err)
	}
	return &Renewer{cc: cc, version: version}, nil
}

// RenewCertificate exchanges a CSR for a re-signed certificate. The agent's new
// private key never leaves the host — only the CSR is sent, exactly as at
// enrollment (threat-model §5.1).
func (r *Renewer) RenewCertificate(ctx context.Context, serverID string, csrPEM []byte) ([]byte, error) {
	resp, err := agentv1.NewEnrollmentServiceClient(r.cc).Renew(ctx, &agentv1.RenewRequest{
		ServerId:     serverID,
		CsrPem:       csrPEM,
		AgentVersion: r.version,
	})
	if err != nil {
		return nil, fmt.Errorf("conn: renewing certificate: %w", err)
	}
	if len(resp.GetCertificatePem()) == 0 {
		return nil, fmt.Errorf("conn: renewing certificate: plane returned no certificate")
	}
	return resp.GetCertificatePem(), nil
}

// Close releases the renewal connection.
func (r *Renewer) Close() error { return r.cc.Close() }

// busOptions is the connection contract with the plane's authorizer, kept
// separate so a test can assert it without a server. The inbox prefix is
// load-bearing: the bus grants this identity Subscribe on
// subjects.InboxForServer(id) and nothing under the shared "_INBOX", so every
// request/reply — the desired-state sync, the JetStream pull and API calls —
// must reply into the agent's own scope (threat-model §5.2;
// control-plane-hardening.md §1).
func busOptions(serverID string, tlsCfg *tls.Config, log *slog.Logger) []nats.Option {
	return []nats.Option{
		nats.Secure(tlsCfg),
		nats.Name("cypher-agent/" + serverID),
		nats.CustomInboxPrefix(subjects.InboxPrefix(serverID)),
		nats.MaxReconnects(-1),
		nats.ReconnectWait(2 * time.Second),
		nats.DisconnectErrHandler(func(_ *nats.Conn, err error) {
			if err != nil {
				log.Warn("bus disconnected, will retry", "error", err)
			}
		}),
		nats.ReconnectHandler(func(_ *nats.Conn) {
			log.Info("bus reconnected")
		}),
		// Without this, the client writes async errors (e.g. authorization
		// violations after revocation) straight to stderr, bypassing slog.
		nats.ErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, err error) {
			log.Error("bus error", "error", err)
		}),
	}
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
