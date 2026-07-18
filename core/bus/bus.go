// Package bus is the embedded NATS JetStream event bus (ADR-003). It runs a
// NATS server in-process inside cypherd — no separate service to operate — and
// exposes the subject families in pkg/subjects.
//
// Security (threat-model §5.2, §5.4): the network listener is mTLS with
// RequireAndVerifyClientCert, so only holders of a plane-issued certificate
// connect; a per-agent authorization callback then (a) refuses identities that
// are no longer enrolled — a deleted server's still-valid certificate is
// refused, which is the revocation check of threat-model §8 requirement 6 —
// and (b) scopes each identity to its own subjects. DisconnectAgent completes
// revocation for connections that were already open. The control plane's own
// connection is in-process (no TLS, never on the network).
//
// Storage: the Phase 1 STATE stream is memory-backed with a short max age.
// Heartbeats are transient and re-sent every interval, so nothing durable is
// needed — and this deliberately avoids the continuous disk write-churn that
// the Dokploy baseline measured at 22.76 GiB on an idle box (research/dokploy.md,
// threat-model §5.9). The durable WORK stream arrives with Phase 2.
package bus

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

const (
	streamState      = "STATE"
	planeUser        = "cypherd-control-plane"
	heartbeatDurable = "plane-heartbeats"
	readyTimeout     = 10 * time.Second
)

// AgentAuthorizer answers whether a certificate identity is still an enrolled
// server (consumer-defined; *store.Store satisfies it). A "no" refuses the
// connection even though the certificate itself is valid — this is what makes
// deleting a server revoke its agent (threat-model §8 requirement 6).
type AgentAuthorizer interface {
	AgentEnrolled(ctx context.Context, serverID string) (bool, error)
}

// authTimeout bounds the enrollment lookup during connection auth.
const authTimeout = 3 * time.Second

// Options configures the embedded bus.
type Options struct {
	// ListenAddr is the host:port for the mTLS NATS listener (e.g. ":4222").
	ListenAddr string
	// TLSConfig must require and verify agent client certificates
	// (pki.ServerTLSConfig). Required.
	TLSConfig *tls.Config
	// Authorizer decides whether a presented identity is still enrolled.
	// Required: without it a deleted server's certificate would keep working
	// until expiry.
	Authorizer AgentAuthorizer
	// Log receives connection-refusal events. Required.
	Log *slog.Logger
	// StateMaxAge bounds how long heartbeats linger; defaults to 10 minutes.
	StateMaxAge time.Duration
	// StateMaxMemory caps the STATE stream's memory; defaults to 32 MiB.
	StateMaxMemory int64
}

// Bus owns the embedded NATS server and the control plane's in-process client.
type Bus struct {
	ns *natsserver.Server
	nc *nats.Conn
	js jetstream.JetStream
}

// Start boots the embedded NATS server, connects the plane in-process, and
// ensures the STATE stream exists.
func Start(ctx context.Context, opts Options) (*Bus, error) {
	if opts.TLSConfig == nil {
		return nil, fmt.Errorf("bus: TLSConfig is required (mTLS is mandatory, ENGINEERING rule 23)")
	}
	if opts.Authorizer == nil {
		return nil, fmt.Errorf("bus: Authorizer is required (revocation check, threat-model §8 req 6)")
	}
	if opts.Log == nil {
		return nil, fmt.Errorf("bus: Log is required")
	}
	host, port, err := parseListenAddr(opts.ListenAddr)
	if err != nil {
		return nil, err
	}
	maxMem := opts.StateMaxMemory
	if maxMem == 0 {
		maxMem = 32 << 20
	}

	nopts := &natsserver.Options{
		ServerName:                 "cypherd",
		Host:                       host,
		Port:                       port,
		JetStream:                  true,
		JetStreamMaxMemory:         maxMem,
		JetStreamMaxStore:          maxMem, // Phase 1 uses memory storage only
		NoLog:                      true,
		NoSigs:                     true,
		TLSConfig:                  opts.TLSConfig,
		TLSVerify:                  true,
		CustomClientAuthentication: &certAuth{authorizer: opts.Authorizer, log: opts.Log},
	}
	ns, err := natsserver.NewServer(nopts)
	if err != nil {
		return nil, fmt.Errorf("bus: constructing NATS server: %w", err)
	}
	ns.Start()
	if !ns.ReadyForConnections(readyTimeout) {
		ns.Shutdown()
		return nil, fmt.Errorf("bus: NATS server not ready within %s", readyTimeout)
	}

	nc, err := nats.Connect("", nats.InProcessServer(ns))
	if err != nil {
		ns.Shutdown()
		return nil, fmt.Errorf("bus: in-process connect: %w", err)
	}
	js, err := jetstream.New(nc)
	if err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: jetstream context: %w", err)
	}

	maxAge := opts.StateMaxAge
	if maxAge == 0 {
		maxAge = 10 * time.Minute
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamState,
		Subjects:  []string{subjects.StatePrefix + ">"},
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		MaxAge:    maxAge,
		MaxBytes:  maxMem,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: creating STATE stream: %w", err)
	}

	return &Bus{ns: ns, nc: nc, js: js}, nil
}

// ConsumeHeartbeats delivers each heartbeat payload to handle. It returns a
// consume context whose Stop method the caller invokes to unsubscribe. The
// handler is called from NATS goroutines; it must be safe for concurrent use.
func (b *Bus) ConsumeHeartbeats(ctx context.Context, handle func(data []byte)) (jetstream.ConsumeContext, error) {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamState, jetstream.ConsumerConfig{
		Durable:       heartbeatDurable,
		FilterSubject: subjects.HeartbeatAll,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: creating heartbeat consumer: %w", err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		handle(msg.Data())
		_ = msg.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("bus: starting heartbeat consume: %w", err)
	}
	return cc, nil
}

// ClientURL is the in-process/localhost URL, exposed for tests.
func (b *Bus) ClientURL() string { return b.ns.ClientURL() }

// Addr returns the resolved listener address (host:port). Useful when the
// configured port was 0 (tests) and for computing the advertised URL.
func (b *Bus) Addr() net.Addr { return b.ns.Addr() }

// Close shuts down the plane client and the embedded server.
func (b *Bus) Close() {
	if b.nc != nil {
		b.nc.Close()
	}
	if b.ns != nil {
		b.ns.Shutdown()
		b.ns.WaitForShutdown()
	}
}

// DisconnectAgent severs any open connection held by serverID. Combined with
// the connect-time enrollment check this completes revocation: a deleted
// server is kicked now and refused on reconnect (threat-model §8 req 6).
// Disconnecting an agent with no open connection is a no-op.
func (b *Bus) DisconnectAgent(serverID string) error {
	connz, err := b.ns.Connz(&natsserver.ConnzOptions{Username: true, User: serverID})
	if err != nil {
		return fmt.Errorf("bus: listing connections for %s: %w", serverID, err)
	}
	for _, conn := range connz.Conns {
		if err := b.ns.DisconnectClientByID(conn.Cid); err != nil {
			return fmt.Errorf("bus: disconnecting %s (cid %d): %w", serverID, conn.Cid, err)
		}
	}
	return nil
}

// certAuth authorizes NATS connections. It is the enforcement point for
// threat-model §5.2 (each agent identity may publish only its own state) and
// §8 req 6 (an identity that is no longer enrolled is refused, even with a
// valid certificate).
type certAuth struct {
	authorizer AgentAuthorizer
	log        *slog.Logger
}

func (a *certAuth) Check(c natsserver.ClientAuthentication) bool {
	tlsState := c.GetTLSConnectionState()
	if tlsState == nil {
		// No TLS state ⇒ the in-process control-plane connection. Network
		// clients cannot reach here without a verified client certificate (the
		// listener sets RequireAndVerifyClientCert), so this is the trusted
		// plane; grant it the default (unrestricted) permissions.
		c.RegisterUser(&natsserver.User{Username: planeUser})
		return true
	}
	if len(tlsState.PeerCertificates) == 0 {
		return false
	}
	cn := tlsState.PeerCertificates[0].Subject.CommonName
	if cn == "" {
		return false
	}
	// Revocation check: the certificate is cryptographically valid, but the
	// server behind it must still exist and be enrolled. Fail closed on error.
	ctx, cancel := context.WithTimeout(context.Background(), authTimeout)
	defer cancel()
	enrolled, err := a.authorizer.AgentEnrolled(ctx, cn)
	if err != nil {
		a.log.Error("bus auth: enrollment lookup failed, refusing connection", "server_id", cn, "error", err)
		return false
	}
	if !enrolled {
		a.log.Warn("bus auth: refused connection from revoked or unknown identity", "server_id", cn)
		return false
	}
	// Scope the agent to its own subjects only.
	c.RegisterUser(&natsserver.User{
		Username: cn,
		Permissions: &natsserver.Permissions{
			Publish: &natsserver.SubjectPermission{
				Allow: []string{subjects.Heartbeat(cn), subjects.StateForServer(cn)},
			},
			Subscribe: &natsserver.SubjectPermission{
				Allow: []string{subjects.WorkForServer(cn), "_INBOX.>"},
			},
		},
	})
	return true
}

func parseListenAddr(addr string) (host string, port int, err error) {
	if addr == "" {
		return "", 0, fmt.Errorf("bus: ListenAddr is required")
	}
	h, p, err := net.SplitHostPort(addr)
	if err != nil {
		return "", 0, fmt.Errorf("bus: parsing listen addr %q: %w", addr, err)
	}
	if h == "" {
		h = "0.0.0.0"
	}
	pn, err := strconv.Atoi(p)
	if err != nil {
		return "", 0, fmt.Errorf("bus: parsing listen port %q: %w", p, err)
	}
	return h, pn, nil
}
