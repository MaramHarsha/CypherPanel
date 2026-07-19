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
// Storage: the STATE stream is memory-backed with a short max age —
// heartbeats and status reports are transient and re-sent, and this avoids
// the continuous disk write-churn the Dokploy baseline measured at 22.76 GiB
// on an idle box (research/dokploy.md, threat-model §5.9). The WORK stream is
// file-backed with explicit retention bounds (§8 req 9): work items must
// survive a plane restart, and WorkQueue retention deletes each item when its
// server's consumer acknowledges it.
package bus

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"time"

	natsserver "github.com/nats-io/nats-server/v2/server"
	"github.com/nats-io/nats.go"
	"github.com/nats-io/nats.go/jetstream"

	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

const (
	streamState        = "STATE"
	streamWork         = "WORK"
	streamLogs         = "LOGS"
	streamRuntimeLogs  = "RUNTIME_LOGS"
	streamRelay        = "RELAY"
	planeUser          = "cypherd-control-plane"
	heartbeatDurable   = "plane-heartbeats"
	deployEventDurable = "plane-deploy-events"
	appStatusDurable   = "plane-app-status"
	readyTimeout       = 10 * time.Second
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
	// WorkMaxAge bounds how long an undelivered work item may wait; defaults
	// to 24 hours (threat-model §8 req 9: explicit retention limits).
	WorkMaxAge time.Duration
	// WorkMaxBytes caps the file-backed WORK stream; defaults to 256 MiB.
	WorkMaxBytes int64
	// LogsMaxAge bounds how long build log lines are retained for SSE
	// replay; defaults to 30 minutes. Memory-backed like STATE — bounded
	// retention without disk churn (threat-model §5.9, §8 req 9). Runtime
	// logs live on the file-backed RUNTIME_LOGS stream instead
	// (bounded-log-retention.md §2).
	LogsMaxAge time.Duration
	// LogsMaxBytes caps the LOGS stream's memory; defaults to 64 MiB.
	LogsMaxBytes int64
	// RuntimeLogsMaxAge bounds how long runtime log lines are retained on
	// disk (bounded-log-retention.md §2); defaults to 24 hours.
	RuntimeLogsMaxAge time.Duration
	// RuntimeLogsMaxBytes caps the file-backed RUNTIME_LOGS stream;
	// defaults to 512 MiB.
	RuntimeLogsMaxBytes int64
	// StoreDir is where JetStream keeps the file-backed WORK and
	// RUNTIME_LOGS streams. Defaults to the NATS default when empty
	// (tests); production sets it.
	StoreDir string
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

	workMaxBytes := opts.WorkMaxBytes
	if workMaxBytes == 0 {
		workMaxBytes = 256 << 20
	}
	logsMaxBytes := opts.LogsMaxBytes
	if logsMaxBytes == 0 {
		logsMaxBytes = 64 << 20
	}
	runtimeLogsMaxBytes := opts.RuntimeLogsMaxBytes
	if runtimeLogsMaxBytes == 0 {
		runtimeLogsMaxBytes = 512 << 20
	}
	runtimeLogsMaxAge := opts.RuntimeLogsMaxAge
	if runtimeLogsMaxAge == 0 {
		runtimeLogsMaxAge = 24 * time.Hour
	}
	nopts := &natsserver.Options{
		ServerName: "cypherd",
		Host:       host,
		Port:       port,
		JetStream:  true,
		// The server-level memory cap must cover every memory-backed stream:
		// STATE (maxMem), LOGS (logsMaxBytes), and RELAY (logsMaxBytes).
		JetStreamMaxMemory:         maxMem + logsMaxBytes*2,
		JetStreamMaxStore:          workMaxBytes + runtimeLogsMaxBytes, // file storage backs WORK and RUNTIME_LOGS
		StoreDir:                   opts.StoreDir,
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
	// The STATE stream captures only the durable state families — heartbeats,
	// deploy events, and app-status observations. It deliberately does NOT
	// cover state.*.sync: sync is core-NATS request/reply, and if JetStream
	// captured that subject it would answer the agent's request with a stream
	// PubAck instead of letting the plane's responder reply (the agent would
	// then fail to unmarshal the PubAck as a DesiredState). Keep this list in
	// step with the state.* subjects in pkg/subjects.
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamState,
		Subjects:  []string{subjects.HeartbeatAll, subjects.DeployStateAll, subjects.AppStateAll},
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

	// The WORK stream is file-backed — work items must survive a plane
	// restart (docs/features/application-deploy.md §3) — with explicit
	// retention bounds (threat-model §8 req 9). WorkQueue retention: each
	// item is removed once its server's consumer acknowledges it; per-server
	// consumers have non-overlapping filters, which is what WorkQueuePolicy
	// requires.
	workMaxAge := opts.WorkMaxAge
	if workMaxAge == 0 {
		workMaxAge = 24 * time.Hour
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamWork,
		Subjects:  []string{subjects.WorkPrefix + ">"},
		Storage:   jetstream.FileStorage,
		Retention: jetstream.WorkQueuePolicy,
		Discard:   jetstream.DiscardOld,
		MaxAge:    workMaxAge,
		MaxBytes:  workMaxBytes,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: creating WORK stream: %w", err)
	}

	// The LOGS stream retains recent build/runtime log lines so an SSE client
	// connecting mid- (or just after) a build replays what it missed, then
	// tails live (application-deploy.md §5: bounded retention on the plane).
	logsMaxAge := opts.LogsMaxAge
	if logsMaxAge == 0 {
		logsMaxAge = 30 * time.Minute
	}
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamLogs,
		Subjects:  []string{subjects.BuildLogAll},
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		MaxAge:    logsMaxAge,
		MaxBytes:  logsMaxBytes,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: creating LOGS stream: %w", err)
	}

	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamRuntimeLogs,
		Subjects:  []string{subjects.RuntimeLogAll},
		Storage:   jetstream.FileStorage,
		Retention: jetstream.LimitsPolicy,
		Discard:   jetstream.DiscardOld,
		MaxAge:    runtimeLogsMaxAge,
		MaxBytes:  runtimeLogsMaxBytes,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: creating RUNTIME_LOGS stream: %w", err)
	}

	// The RELAY stream holds in-flight images during multi-server distribution
	// (ADR-008). It is memory-backed and expires quickly; chunks are pulled by
	// the target and then dropped.
	if _, err := js.CreateOrUpdateStream(ctx, jetstream.StreamConfig{
		Name:      streamRelay,
		Subjects:  []string{subjects.RelayPrefix + ">"},
		Storage:   jetstream.MemoryStorage,
		Retention: jetstream.WorkQueuePolicy,
		Discard:   jetstream.DiscardOld,
		MaxAge:    15 * time.Minute,
		MaxBytes:  logsMaxBytes,
	}); err != nil {
		nc.Close()
		ns.Shutdown()
		return nil, fmt.Errorf("bus: creating RELAY stream: %w", err)
	}

	return &Bus{ns: ns, nc: nc, js: js}, nil
}

// SubscribeLogs delivers the retained history of one build-log subject and
// then its live tail to handle, until stop is called. Backed by an ephemeral
// ordered consumer on the LOGS stream, so each SSE client gets its own cursor
// and nothing is retained on its behalf after stop.
func (b *Bus) SubscribeLogs(ctx context.Context, subject string, handle func(data []byte)) (stop func(), err error) {
	return b.subscribeStream(ctx, streamLogs, subject, handle)
}

// SubscribeRuntimeLogs is SubscribeLogs for runtime logs, backed by the
// file-backed RUNTIME_LOGS stream (bounded-log-retention.md §4) so history
// within the retention window survives a plane restart.
func (b *Bus) SubscribeRuntimeLogs(ctx context.Context, subject string, handle func(data []byte)) (stop func(), err error) {
	return b.subscribeStream(ctx, streamRuntimeLogs, subject, handle)
}

func (b *Bus) subscribeStream(ctx context.Context, stream, subject string, handle func(data []byte)) (stop func(), err error) {
	cons, err := b.js.OrderedConsumer(ctx, stream, jetstream.OrderedConsumerConfig{
		FilterSubjects: []string{subject},
		DeliverPolicy:  jetstream.DeliverAllPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: creating log consumer for %s: %w", subject, err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		handle(msg.Data())
	})
	if err != nil {
		return nil, fmt.Errorf("bus: starting log consume for %s: %w", subject, err)
	}
	return cc.Stop, nil
}

// EnsureWorkConsumer creates (or keeps) the durable consumer holding one
// server's work-item cursor. The plane owns consumer lifecycle; the agent is
// only granted read/ack on it (threat-model §5.2). Called at server creation
// and re-asserted on boot, so a rebuilt plane heals its consumers.
func (b *Bus) EnsureWorkConsumer(ctx context.Context, serverID string) error {
	_, err := b.js.CreateOrUpdateConsumer(ctx, streamWork, jetstream.ConsumerConfig{
		Durable:       subjects.WorkConsumer(serverID),
		FilterSubject: subjects.WorkForServer(serverID),
		AckPolicy:     jetstream.AckExplicitPolicy,
		AckWait:       2 * time.Minute,
	})
	if err != nil {
		return fmt.Errorf("bus: ensuring work consumer for %s: %w", serverID, err)
	}
	return nil
}

// DeleteWorkConsumer removes a deleted server's work consumer. Missing is
// fine — deletion must be idempotent.
func (b *Bus) DeleteWorkConsumer(ctx context.Context, serverID string) error {
	err := b.js.DeleteConsumer(ctx, streamWork, subjects.WorkConsumer(serverID))
	if err != nil && !errors.Is(err, jetstream.ErrConsumerNotFound) {
		return fmt.Errorf("bus: deleting work consumer for %s: %w", serverID, err)
	}
	return nil
}

// PublishWork publishes one work item. msgID is the idempotency key
// (deployment id + stage): JetStream deduplicates re-publishes inside the
// dedup window, and consumers are idempotent regardless (ENGINEERING rule 12).
func (b *Bus) PublishWork(ctx context.Context, subject, msgID string, data []byte) error {
	if _, err := b.js.Publish(ctx, subject, data, jetstream.WithMsgID(msgID)); err != nil {
		return fmt.Errorf("bus: publishing work to %s: %w", subject, err)
	}
	return nil
}

// ConsumeDeployEvents delivers each DeployEvent payload (with its server id
// parsed from the subject) to handle. Same contract as ConsumeHeartbeats.
func (b *Bus) ConsumeDeployEvents(ctx context.Context, handle func(serverID string, data []byte)) (jetstream.ConsumeContext, error) {
	return b.consumeState(ctx, deployEventDurable, subjects.DeployStateAll, handle)
}

// ConsumeAppStatus delivers each AppStatus payload (with its server id parsed
// from the subject) to handle.
func (b *Bus) ConsumeAppStatus(ctx context.Context, handle func(serverID string, data []byte)) (jetstream.ConsumeContext, error) {
	return b.consumeState(ctx, appStatusDurable, subjects.AppStateAll, handle)
}

func (b *Bus) consumeState(ctx context.Context, durable, filter string, handle func(serverID string, data []byte)) (jetstream.ConsumeContext, error) {
	cons, err := b.js.CreateOrUpdateConsumer(ctx, streamState, jetstream.ConsumerConfig{
		Durable:       durable,
		FilterSubject: filter,
		AckPolicy:     jetstream.AckExplicitPolicy,
	})
	if err != nil {
		return nil, fmt.Errorf("bus: creating %s consumer: %w", durable, err)
	}
	cc, err := cons.Consume(func(msg jetstream.Msg) {
		// Subjects are state.<server_id>.<...>; the server segment is the
		// trusted identity because the per-agent publish grant pins it.
		parts := strings.SplitN(msg.Subject(), ".", 3)
		if len(parts) >= 2 {
			handle(parts[1], msg.Data())
		}
		_ = msg.Ack()
	})
	if err != nil {
		return nil, fmt.Errorf("bus: starting %s consume: %w", durable, err)
	}
	return cc, nil
}

// RespondDesiredState answers agents' desired-state sync requests: resolve
// receives the requesting server's id and returns the marshaled DesiredState.
// The subscription lives until the bus closes.
func (b *Bus) RespondDesiredState(resolve func(serverID string) ([]byte, error)) error {
	_, err := b.nc.Subscribe(subjects.SyncAll, func(msg *nats.Msg) {
		parts := strings.SplitN(msg.Subject, ".", 3)
		if len(parts) < 3 || msg.Reply == "" {
			return
		}
		data, err := resolve(parts[1])
		if err != nil {
			// No reply → the agent times out and retries; the plane logs via
			// resolve's own error path.
			return
		}
		_ = msg.Respond(data)
	})
	if err != nil {
		return fmt.Errorf("bus: subscribing to sync requests: %w", err)
	}
	return nil
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
	// Scope the agent to its own subjects only. Heartbeats, deploy events,
	// app statuses and sync requests all live inside the server's state
	// scope; logs inside its logs scope. The JetStream API grants cover
	// exactly this server's work consumer — reading or acknowledging another
	// server's work is impossible by construction (threat-model §5.2).
	c.RegisterUser(&natsserver.User{
		Username: cn,
		Permissions: &natsserver.Permissions{
			Publish: &natsserver.SubjectPermission{
				Allow: []string{
					subjects.StateForServer(cn),
					subjects.LogsForServer(cn),
					subjects.WorkConsumerInfo(cn),
					subjects.WorkConsumerNext(cn),
					subjects.WorkConsumerAck(cn),
				},
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
