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
	"github.com/nats-io/nats.go/jetstream"
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
		StoreDir:   t.TempDir(), // file-backed WORK stream stays in the test dir
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

// agentJetStream returns a JetStream context over an agent connection.
func agentJetStream(t *testing.T, nc *nats.Conn) jetstream.JetStream {
	t.Helper()
	js, err := jetstream.New(nc)
	if err != nil {
		t.Fatalf("jetstream.New: %v", err)
	}
	return js
}

// TestWorkItemDeliveredAndSurvivesUntilAck: the plane publishes a work item;
// the agent consumes it via its plane-created durable consumer. Unacked items
// redeliver; acked items are gone (WorkQueue retention).
func TestWorkItemDeliveredAndAcked(t *testing.T) {
	const srv = "srv_alpha"
	b, ca, _ := startTestBus(t, srv)
	ctx := context.Background()

	if err := b.EnsureWorkConsumer(ctx, srv); err != nil {
		t.Fatalf("EnsureWorkConsumer: %v", err)
	}
	work := &agentv1.RolloutWork{DeploymentId: "dep_1", Spec: &agentv1.AppSpec{AppId: "app_1", RevisionId: "rev_1"}}
	data, err := proto.Marshal(work)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if err := b.PublishWork(ctx, subjects.Rollout(srv), "dep_1.rollout", data); err != nil {
		t.Fatalf("PublishWork: %v", err)
	}

	nc := agentConn(t, b, ca, srv)
	js := agentJetStream(t, nc)
	cons, err := js.Consumer(ctx, "WORK", subjects.WorkConsumer(srv))
	if err != nil {
		t.Fatalf("agent consumer lookup: %v", err)
	}
	msgs, err := cons.Fetch(1, jetstream.FetchMaxWait(5*time.Second))
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	var got jetstream.Msg
	for m := range msgs.Messages() {
		got = m
	}
	if got == nil {
		t.Fatal("no work item delivered")
	}
	if got.Subject() != subjects.Rollout(srv) {
		t.Fatalf("subject = %q, want %q", got.Subject(), subjects.Rollout(srv))
	}
	var rw agentv1.RolloutWork
	if err := proto.Unmarshal(got.Data(), &rw); err != nil || rw.GetDeploymentId() != "dep_1" {
		t.Fatalf("payload = %+v, %v", &rw, err)
	}
	if err := got.Ack(); err != nil {
		t.Fatalf("ack: %v", err)
	}

	// Duplicate publish with the same msg id is deduplicated.
	if err := b.PublishWork(ctx, subjects.Rollout(srv), "dep_1.rollout", data); err != nil {
		t.Fatalf("re-PublishWork: %v", err)
	}
	msgs, err = cons.Fetch(1, jetstream.FetchMaxWait(time.Second))
	if err != nil {
		t.Fatalf("fetch after ack: %v", err)
	}
	for m := range msgs.Messages() {
		t.Fatalf("unexpected redelivery after ack+dedup: %s", m.Subject())
	}
}

// TestAgentCannotReadAnotherServersWork: the JS API grants pin an agent to its
// own consumer; fetching from another server's consumer must fail.
func TestAgentCannotReadAnotherServersWork(t *testing.T) {
	b, ca, _ := startTestBus(t, "srv_alpha", "srv_beta")
	ctx := context.Background()
	for _, id := range []string{"srv_alpha", "srv_beta"} {
		if err := b.EnsureWorkConsumer(ctx, id); err != nil {
			t.Fatalf("EnsureWorkConsumer(%s): %v", id, err)
		}
	}

	nc := agentConn(t, b, ca, "srv_alpha")
	js := agentJetStream(t, nc)
	// Consumer lookup for the other server must be refused (INFO permission).
	if _, err := js.Consumer(ctx, "WORK", subjects.WorkConsumer("srv_beta")); err == nil {
		t.Fatal("srv_alpha read srv_beta's consumer info")
	}
}

// TestDesiredStateSync: an agent's sync request is answered with the plane's
// resolved DesiredState for exactly that server.
func TestDesiredStateSync(t *testing.T) {
	const srv = "srv_alpha"
	b, ca, _ := startTestBus(t, srv)

	err := b.RespondDesiredState(func(serverID string) ([]byte, error) {
		return proto.Marshal(&agentv1.DesiredState{Specs: []*agentv1.AppSpec{{AppId: "app-for-" + serverID}}})
	})
	if err != nil {
		t.Fatalf("RespondDesiredState: %v", err)
	}

	nc := agentConn(t, b, ca, srv)
	resp, err := nc.Request(subjects.Sync(srv), nil, 5*time.Second)
	if err != nil {
		t.Fatalf("sync request: %v", err)
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(resp.Data, &ds); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(ds.Specs) != 1 || ds.Specs[0].GetAppId() != "app-for-"+srv {
		t.Fatalf("desired state = %+v, want the resolver's answer for %s", ds.Specs, srv)
	}
}

// TestDeployEventAndAppStatusConsumers: agent publications on the deploy and
// app-status subjects reach the plane consumers with the server id parsed
// from the (permission-pinned) subject.
func TestDeployEventAndAppStatusConsumers(t *testing.T) {
	const srv = "srv_alpha"
	b, ca, _ := startTestBus(t, srv)
	ctx := context.Background()

	events := make(chan string, 1)
	cc1, err := b.ConsumeDeployEvents(ctx, func(serverID string, data []byte) {
		var ev agentv1.DeployEvent
		if proto.Unmarshal(data, &ev) == nil {
			events <- serverID + "/" + ev.GetDeploymentId()
		}
	})
	if err != nil {
		t.Fatalf("ConsumeDeployEvents: %v", err)
	}
	defer cc1.Stop()

	statuses := make(chan string, 1)
	cc2, err := b.ConsumeAppStatus(ctx, func(serverID string, data []byte) {
		var st agentv1.AppStatus
		if proto.Unmarshal(data, &st) == nil {
			statuses <- serverID + "/" + st.GetAppId() + "/" + st.GetState()
		}
	})
	if err != nil {
		t.Fatalf("ConsumeAppStatus: %v", err)
	}
	defer cc2.Stop()

	nc := agentConn(t, b, ca, srv)
	ev, _ := proto.Marshal(&agentv1.DeployEvent{DeploymentId: "dep_9"})
	if err := nc.Publish(subjects.DeployState(srv), ev); err != nil {
		t.Fatalf("publish deploy event: %v", err)
	}
	st, _ := proto.Marshal(&agentv1.AppStatus{AppId: "app_1", State: "running"})
	if err := nc.Publish(subjects.AppState(srv, "app_1"), st); err != nil {
		t.Fatalf("publish app status: %v", err)
	}

	select {
	case got := <-events:
		if got != srv+"/dep_9" {
			t.Fatalf("deploy event = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("deploy event never consumed")
	}
	select {
	case got := <-statuses:
		if got != srv+"/app_1/running" {
			t.Fatalf("app status = %q", got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("app status never consumed")
	}
}
