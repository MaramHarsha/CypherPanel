package bus

import (
	"context"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// fakeAuthorizer is an in-memory AgentAuthorizer whose enrollment set can be
// mutated mid-test (to simulate a server being deleted).
type fakeAuthorizer struct {
	mu       sync.Mutex
	enrolled map[string]bool
}

func newFakeAuthorizer(ids ...string) *fakeAuthorizer {
	f := &fakeAuthorizer{enrolled: map[string]bool{}}
	for _, id := range ids {
		f.enrolled[id] = true
	}
	return f
}

func (f *fakeAuthorizer) AgentEnrolled(_ context.Context, serverID string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.enrolled[serverID], nil
}

func (f *fakeAuthorizer) revoke(serverID string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.enrolled, serverID)
}

// startTestBus brings up an embedded bus on a random localhost port with a
// fresh CA, returning the bus, the CA (for minting agent certs), and the
// mutable authorizer.
func startTestBus(t *testing.T, enrolledIDs ...string) (*Bus, *pki.CA, *fakeAuthorizer) {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	serverCert, serverKey, err := ca.IssueServerCert([]string{"localhost"}, []net.IP{net.IPv4(127, 0, 0, 1)}, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("IssueServerCert: %v", err)
	}
	tlsCfg, err := pki.ServerTLSConfig(serverCert, serverKey, ca.CertPEM())
	if err != nil {
		t.Fatalf("ServerTLSConfig: %v", err)
	}
	authorizer := newFakeAuthorizer(enrolledIDs...)
	b, err := Start(context.Background(), Options{
		ListenAddr: "127.0.0.1:0",
		TLSConfig:  tlsCfg,
		Authorizer: authorizer,
		Log:        slog.New(slog.NewTextHandler(io.Discard, nil)),
	})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Close)
	return b, ca, authorizer
}

// dialAgent attempts to connect to the bus as an agent holding a valid cert
// for serverID, returning the connection or the connect error.
func dialAgent(b *Bus, ca *pki.CA, serverID string, opts ...nats.Option) (*nats.Conn, error) {
	key, csr, err := pki.GenerateAgentKey(serverID)
	if err != nil {
		return nil, err
	}
	certPEM, err := ca.SignAgentCSR(csr, serverID, time.Hour, time.Now())
	if err != nil {
		return nil, err
	}
	clientCfg, err := pki.ClientTLSConfig(certPEM, key, ca.CertPEM(), "localhost")
	if err != nil {
		return nil, err
	}
	url := "tls://" + b.Addr().String()
	opts = append([]nats.Option{nats.Secure(clientCfg), nats.Timeout(5 * time.Second)}, opts...)
	return nats.Connect(url, opts...)
}

// agentConn dials the bus as an enrolled agent with the given server ID.
func agentConn(t *testing.T, b *Bus, ca *pki.CA, serverID string, opts ...nats.Option) *nats.Conn {
	t.Helper()
	nc, err := dialAgent(b, ca, serverID, opts...)
	if err != nil {
		t.Fatalf("agent connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestBusDeliversHeartbeatToConsumer(t *testing.T) {
	b, ca, _ := startTestBus(t, "srv_alpha")

	got := make(chan []byte, 1)
	cc, err := b.ConsumeHeartbeats(context.Background(), func(data []byte) {
		select {
		case got <- data:
		default:
		}
	})
	if err != nil {
		t.Fatalf("ConsumeHeartbeats: %v", err)
	}
	defer cc.Stop()

	nc := agentConn(t, b, ca, "srv_alpha")
	payload, err := proto.Marshal(&agentv1.Heartbeat{ServerId: "srv_alpha", Status: agentv1.AgentStatus_AGENT_STATUS_READY})
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if err := nc.Publish(subjects.Heartbeat("srv_alpha"), payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if err := nc.Flush(); err != nil {
		t.Fatalf("Flush: %v", err)
	}

	select {
	case data := <-got:
		var hb agentv1.Heartbeat
		if err := proto.Unmarshal(data, &hb); err != nil {
			t.Fatalf("Unmarshal: %v", err)
		}
		if hb.GetServerId() != "srv_alpha" {
			t.Errorf("got server id %q, want srv_alpha", hb.GetServerId())
		}
	case <-time.After(5 * time.Second):
		t.Fatal("did not receive heartbeat within 5s")
	}
}

// TestBusRejectsCrossServerPublish proves the per-identity authorization: an
// agent enrolled as srv_alpha may not publish another server's heartbeat
// subject (threat-model §5.2). The disallowed message must never reach the
// consumer.
func TestBusRejectsCrossServerPublish(t *testing.T) {
	b, ca, _ := startTestBus(t, "srv_alpha", "srv_victim")

	got := make(chan []byte, 1)
	cc, err := b.ConsumeHeartbeats(context.Background(), func(data []byte) {
		select {
		case got <- data:
		default:
		}
	})
	if err != nil {
		t.Fatalf("ConsumeHeartbeats: %v", err)
	}
	defer cc.Stop()

	permErr := make(chan error, 1)
	nc := agentConn(t, b, ca, "srv_alpha")
	nc.SetErrorHandler(func(_ *nats.Conn, _ *nats.Subscription, e error) {
		select {
		case permErr <- e:
		default:
		}
	})

	// srv_alpha attempts to publish srv_victim's subject.
	payload, _ := proto.Marshal(&agentv1.Heartbeat{ServerId: "srv_victim", Status: agentv1.AgentStatus_AGENT_STATUS_READY})
	if err := nc.Publish(subjects.Heartbeat("srv_victim"), payload); err != nil {
		t.Fatalf("Publish: %v", err)
	}
	_ = nc.Flush()

	select {
	case <-got:
		t.Fatal("consumer received a cross-server publish that should have been denied")
	case <-time.After(1 * time.Second):
		// Correct: the message was denied and never delivered.
	}
}

// TestBusRefusesRevokedIdentity proves the revocation check (threat-model §8
// req 6): a certificate that is cryptographically valid but whose server is
// not (or no longer) enrolled cannot connect.
func TestBusRefusesRevokedIdentity(t *testing.T) {
	b, ca, _ := startTestBus(t, "srv_alpha") // srv_ghost is not enrolled

	if nc, err := dialAgent(b, ca, "srv_ghost"); err == nil {
		nc.Close()
		t.Fatal("bus accepted a valid certificate for an unenrolled identity")
	}

	// Sanity: the same CA's cert for an enrolled identity does connect.
	nc := agentConn(t, b, ca, "srv_alpha")
	if !nc.IsConnected() {
		t.Fatal("enrolled agent failed to connect")
	}
}

// TestBusDisconnectAgentSeversConnection proves revocation reaches live
// connections: after the server is revoked and DisconnectAgent is called, the
// open connection closes and a reconnect with the same still-valid
// certificate is refused (threat-model §8 req 6).
func TestBusDisconnectAgentSeversConnection(t *testing.T) {
	b, ca, authorizer := startTestBus(t, "srv_alpha")

	closed := make(chan struct{})
	nc := agentConn(t, b, ca, "srv_alpha",
		nats.NoReconnect(),
		nats.ClosedHandler(func(*nats.Conn) { close(closed) }),
	)
	if !nc.IsConnected() {
		t.Fatal("agent failed to connect")
	}

	authorizer.revoke("srv_alpha")
	if err := b.DisconnectAgent("srv_alpha"); err != nil {
		t.Fatalf("DisconnectAgent: %v", err)
	}
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("connection not severed within 5s of DisconnectAgent")
	}

	if reNC, err := dialAgent(b, ca, "srv_alpha"); err == nil {
		reNC.Close()
		t.Fatal("revoked identity was allowed to reconnect")
	}
}

// TestBusDisconnectAgentNoConnectionIsNoop: deleting a server whose agent is
// offline must not error.
func TestBusDisconnectAgentNoConnectionIsNoop(t *testing.T) {
	b, _, _ := startTestBus(t)
	if err := b.DisconnectAgent("srv_absent"); err != nil {
		t.Fatalf("DisconnectAgent on absent connection: %v", err)
	}
}
