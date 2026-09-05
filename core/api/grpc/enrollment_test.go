package grpcapi

// Renew authorization tests (agent-identity-and-tls.md §3). The identity that
// matters is the verified client certificate on the connection; the request
// body is only ever a claim, and a claim that disagrees is refused rather than
// quietly re-issued under the caller's real name.

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/enroll"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
	agentv1 "github.com/MaramHarsha/cypherpanel/pkg/proto/cypherpanel/agent/v1"
)

// fakeEnrollStore is the slice of persistence enroll.Service needs, with just
// enough behaviour to exercise renewal.
type fakeEnrollStore struct {
	servers map[string]domain.Server
}

func (f *fakeEnrollStore) GetJoinToken(context.Context, string) (domain.JoinToken, error) {
	return domain.JoinToken{}, store.ErrNotFound
}

func (f *fakeEnrollStore) ConsumeJoinToken(context.Context, string) (domain.JoinToken, error) {
	return domain.JoinToken{}, store.ErrNotFound
}

func (f *fakeEnrollStore) MarkServerEnrolled(_ context.Context, id, hostname, version string) (domain.Server, error) {
	srv := domain.Server{ID: id, Hostname: hostname, AgentVersion: version}
	f.servers[id] = srv
	return srv, nil
}

func (f *fakeEnrollStore) AgentEnrolled(_ context.Context, id string) (bool, error) {
	_, ok := f.servers[id]
	return ok, nil
}

func (f *fakeEnrollStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	srv, ok := f.servers[id]
	if !ok {
		return domain.Server{}, store.ErrNotFound
	}
	return srv, nil
}

func newEnrollmentServer(t *testing.T) (*EnrollmentServer, *fakeEnrollStore) {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	st := &fakeEnrollStore{servers: map[string]domain.Server{
		"srv_a": {ID: "srv_a", Hostname: "box-a", AgentVersion: "v1.0.0"},
		"srv_b": {ID: "srv_b", Hostname: "box-b", AgentVersion: "v1.0.0"},
	}}
	svc := enroll.NewService(st, ca, 90*24*time.Hour, "tls://plane:4222")
	return NewEnrollmentServer(svc, slog.New(slog.NewTextHandler(io.Discard, nil))), st
}

func csr(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := pki.GenerateAgentKey("box")
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	return csrPEM
}

func TestRenewOverMTLSHappyPath(t *testing.T) {
	srv, st := newEnrollmentServer(t)
	before := st.servers["srv_a"]

	resp, err := srv.Renew(agentCtx("srv_a"), &agentv1.RenewRequest{
		ServerId:     "srv_a",
		CsrPem:       csr(t),
		AgentVersion: "v1.2.0",
	})
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if len(resp.GetCertificatePem()) == 0 || len(resp.GetCaPem()) == 0 {
		t.Fatal("renewal returned no material")
	}
	if resp.GetNotAfter() == nil || !resp.GetNotAfter().AsTime().After(time.Now()) {
		t.Fatalf("NotAfter = %v, want a future expiry", resp.GetNotAfter())
	}
	// A renewal is not a re-enrollment: the server row is untouched, so the
	// panel keeps showing when the server actually joined.
	if st.servers["srv_a"] != before {
		t.Fatalf("renewal mutated the server row: %+v", st.servers["srv_a"])
	}
}

func TestRenewWithoutAClientCertificateIsRefused(t *testing.T) {
	srv, _ := newEnrollmentServer(t)

	_, err := srv.Renew(context.Background(), &agentv1.RenewRequest{ServerId: "srv_a", CsrPem: csr(t)})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Renew without a peer certificate = %v, want PermissionDenied", err)
	}
}

func TestRenewRefusesRevokedIdentity(t *testing.T) {
	srv, st := newEnrollmentServer(t)
	delete(st.servers, "srv_a") // the panel deleted the server

	_, err := srv.Renew(agentCtx("srv_a"), &agentv1.RenewRequest{ServerId: "srv_a", CsrPem: csr(t)})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Renew after revocation = %v, want PermissionDenied", err)
	}
}

func TestRenewRefusesCommonNameMismatch(t *testing.T) {
	srv, _ := newEnrollmentServer(t)

	// srv_a's certificate, asking for srv_b's identity.
	_, err := srv.Renew(agentCtx("srv_a"), &agentv1.RenewRequest{ServerId: "srv_b", CsrPem: csr(t)})
	if status.Code(err) != codes.PermissionDenied {
		t.Fatalf("cross-identity renewal = %v, want PermissionDenied", err)
	}
}

func TestRenewRejectsAMalformedCSR(t *testing.T) {
	srv, _ := newEnrollmentServer(t)

	_, err := srv.Renew(agentCtx("srv_a"), &agentv1.RenewRequest{ServerId: "srv_a", CsrPem: []byte("nope")})
	if status.Code(err) != codes.Internal {
		t.Fatalf("malformed CSR = %v, want Internal", err)
	}
}
