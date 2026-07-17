package bus

import (
	"context"
	"net"
	"testing"
	"time"

	"github.com/nats-io/nats.go"
	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

// startTestBus brings up an embedded bus on a random localhost port with a
// fresh CA, returning the bus and the CA (for minting agent certs).
func startTestBus(t *testing.T) (*Bus, *pki.CA) {
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
	b, err := Start(context.Background(), Options{ListenAddr: "127.0.0.1:0", TLSConfig: tlsCfg})
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(b.Close)
	return b, ca
}

// agentConn dials the bus as an enrolled agent with the given server ID.
func agentConn(t *testing.T, b *Bus, ca *pki.CA, serverID string) *nats.Conn {
	t.Helper()
	key, csr, err := pki.GenerateAgentKey(serverID)
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	certPEM, err := ca.SignAgentCSR(csr, serverID, time.Hour, time.Now())
	if err != nil {
		t.Fatalf("SignAgentCSR: %v", err)
	}
	clientCfg, err := pki.ClientTLSConfig(certPEM, key, ca.CertPEM(), "localhost")
	if err != nil {
		t.Fatalf("ClientTLSConfig: %v", err)
	}
	url := "tls://" + b.Addr().String()
	nc, err := nats.Connect(url, nats.Secure(clientCfg), nats.Timeout(5*time.Second))
	if err != nil {
		t.Fatalf("agent connect: %v", err)
	}
	t.Cleanup(nc.Close)
	return nc
}

func TestBusDeliversHeartbeatToConsumer(t *testing.T) {
	b, ca := startTestBus(t)

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
	b, ca := startTestBus(t)

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
