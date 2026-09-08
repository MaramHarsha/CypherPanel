package scheduler

// Panel TLS in desired state and the fleet resync nudge
// (agent-identity-and-tls.md §4). One panel, one ACME account; every node gets
// it inside the desired set it already reads on connect, and a change to it is
// propagated by asking nodes to re-read — never by pushing a second copy.

import (
	"context"
	"errors"
	"testing"
	"time"

	"google.golang.org/protobuf/proto"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
	"github.com/MaramHarsha/cypherpanel/pkg/subjects"
)

func desiredState(t *testing.T, s *Scheduler, serverID string) *agentv1.DesiredState {
	t.Helper()
	data, err := s.DesiredStateFor(context.Background(), serverID)
	if err != nil {
		t.Fatalf("DesiredStateFor: %v", err)
	}
	var ds agentv1.DesiredState
	if err := proto.Unmarshal(data, &ds); err != nil {
		t.Fatalf("unmarshal desired state: %v", err)
	}
	return &ds
}

func TestDesiredStateCarriesThePanelACMEAccount(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.panelTLS = &domain.PanelTLS{
		ACMEEmail:    "ops@example.com",
		ACMECAServer: "https://acme-staging-v02.api.letsencrypt.org/directory",
	}
	s := newScheduler(fs, fb)

	ds := desiredState(t, s, "srv_1")
	if ds.GetTls().GetAcmeEmail() != "ops@example.com" {
		t.Fatalf("acme_email = %q, want ops@example.com", ds.GetTls().GetAcmeEmail())
	}
	if ds.GetTls().GetAcmeCaServer() != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Fatalf("acme_ca_server = %q", ds.GetTls().GetAcmeCaServer())
	}
}

// A panel that has never configured TLS sends no settings at all, which the
// agent reads as "no resolver" — the honest default.
func TestDesiredStateOmitsTLSWhenUnconfigured(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	s := newScheduler(fs, fb)

	if tls := desiredState(t, s, "srv_1").GetTls(); tls.GetAcmeEmail() != "" {
		t.Fatalf("tls = %+v, want nothing on an unconfigured panel", tls)
	}
}

// RequestResync nudges every ENROLLED server, and only those: a server row that
// has never joined has no agent listening on its work subject.
func TestRequestResyncNudgesEveryEnrolledServer(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	enrolled := time.Now()
	fs.servers = []domain.Server{
		{ID: "srv_1", EnrolledAt: &enrolled},
		{ID: "srv_2", EnrolledAt: &enrolled},
		{ID: "srv_pending"}, // created, join command never run
	}
	s := newScheduler(fs, fb)

	if err := s.RequestResync(context.Background(), "panel tls changed"); err != nil {
		t.Fatalf("RequestResync: %v", err)
	}
	got := map[string]bool{}
	for _, p := range fb.work {
		got[p.subject] = true
		var work agentv1.ResyncWork
		if err := proto.Unmarshal(p.data, &work); err != nil {
			t.Fatalf("unmarshal resync: %v", err)
		}
		if work.GetReason() != "panel tls changed" {
			t.Fatalf("reason = %q", work.GetReason())
		}
	}
	if !got[subjects.Resync("srv_1")] || !got[subjects.Resync("srv_2")] {
		t.Fatalf("published on %v, want both enrolled servers", got)
	}
	if got[subjects.Resync("srv_pending")] {
		t.Fatal("nudged a server that never enrolled")
	}
}

// Two changes in quick succession are two nudges: a message id derived from the
// reason alone would let JetStream dedup swallow the second.
func TestRequestResyncUsesDistinctMessageIDs(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	enrolled := time.Now()
	fs.servers = []domain.Server{{ID: "srv_1", EnrolledAt: &enrolled}}
	s := newScheduler(fs, fb)

	if err := s.RequestResync(context.Background(), "panel tls changed"); err != nil {
		t.Fatalf("first RequestResync: %v", err)
	}
	if err := s.RequestResync(context.Background(), "panel tls changed"); err != nil {
		t.Fatalf("second RequestResync: %v", err)
	}
	if len(fb.work) != 2 {
		t.Fatalf("published %d nudges, want 2", len(fb.work))
	}
	if fb.work[0].msgID == fb.work[1].msgID {
		t.Fatalf("both nudges share message id %q; the second would be deduped away", fb.work[0].msgID)
	}
}

// A store that cannot answer must not take TLS down with it — but it must also
// not invent an account. The sync still succeeds with no settings, which makes
// nodes stop promising HTTPS rather than start.
func TestDesiredStateSurvivesATLSReadFailure(t *testing.T) {
	fs, fb := newFakeStore(), &fakeBus{}
	fs.panelTLSErr = errors.New("database is unreachable")
	s := newScheduler(fs, fb)

	if tls := desiredState(t, s, "srv_1").GetTls(); tls.GetAcmeEmail() != "" {
		t.Fatalf("tls = %+v after a read failure, want nothing", tls)
	}
}
