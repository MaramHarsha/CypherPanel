package grpcapi

// Renew authorization tests (agent-identity-and-tls.md §3). The identity that
// matters is the verified client certificate on the connection; the request
// body is only ever a claim, and a claim that disagrees is refused rather than
// quietly re-issued under the caller's real name.

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/MaramHarsha/cypherpanel/core/audit"
	"github.com/MaramHarsha/cypherpanel/core/auth"
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
	return NewEnrollmentServer(svc, nil, slog.New(slog.NewTextHandler(io.Discard, nil))), st
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

// ─── enrollment is an audited event (audit-log.md §4, threat-model §5.3) ─────

// recordingAudit captures what the enrollment handler records.
type recordingAudit struct {
	entries []audit.Entry
	err     error
}

func (r *recordingAudit) Record(_ context.Context, e audit.Entry) (domain.AuditEvent, error) {
	if r.err != nil {
		return domain.AuditEvent{}, r.err
	}
	r.entries = append(r.entries, e)
	return domain.AuditEvent{ID: "aud_test"}, nil
}

// enrollableServer wires a server whose store holds one live join token, so the
// happy path of Enroll can actually run.
func enrollableServer(t *testing.T, rec AuditRecorder) *EnrollmentServer {
	t.Helper()
	ca, err := pki.NewCA(time.Now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	st := &tokenEnrollStore{
		fakeEnrollStore: &fakeEnrollStore{servers: map[string]domain.Server{}},
		token: domain.JoinToken{
			ID: "jt_1", ServerID: "srv_new",
			TokenHash: auth.HashToken("s3cret"),
			ExpiresAt: time.Now().Add(time.Hour),
		},
	}
	svc := enroll.NewService(st, ca, 90*24*time.Hour, "tls://plane:4222")
	return NewEnrollmentServer(svc, rec, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

type tokenEnrollStore struct {
	*fakeEnrollStore
	token    domain.JoinToken
	consumed bool
}

func (f *tokenEnrollStore) GetJoinToken(_ context.Context, id string) (domain.JoinToken, error) {
	if id != f.token.ID || f.consumed {
		return domain.JoinToken{}, store.ErrNotFound
	}
	return f.token, nil
}

func (f *tokenEnrollStore) ConsumeJoinToken(_ context.Context, id string) (domain.JoinToken, error) {
	if id != f.token.ID || f.consumed {
		return domain.JoinToken{}, store.ErrNotFound
	}
	f.consumed = true
	return f.token, nil
}

// The threat model requires enrollment to be "a first-class, audited event"
// (§5.3, §8.1) — until the audit log it was one slog line.
func TestEnrollmentRecordsAnAuditEvent(t *testing.T) {
	rec := &recordingAudit{}
	srv := enrollableServer(t, rec)

	if _, err := srv.Enroll(context.Background(), &agentv1.EnrollRequest{
		JoinToken:    "jt_1.s3cret",
		CsrPem:       csr(t),
		Hostname:     "box-new",
		AgentVersion: "v1.2.0",
	}); err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if len(rec.entries) != 1 {
		t.Fatalf("recorded %d entries, want 1", len(rec.entries))
	}
	e := rec.entries[0]
	if e.Action != audit.ActionServerEnrolled {
		t.Errorf("action = %q, want %q", e.Action, audit.ActionServerEnrolled)
	}
	// The actor is the AGENT: nobody was signed in, and saying otherwise would
	// attribute a machine's action to a person.
	if e.Actor.Kind != domain.AuditActorAgent || e.Actor.Label != "box-new" {
		t.Errorf("actor = %+v, want an agent labelled by its hostname", e.Actor)
	}
	if e.Actor.UserID != "" {
		t.Error("an enrollment was attributed to a user")
	}
	if e.Resource.Kind != audit.ResourceServer || e.Resource.ID != "srv_new" {
		t.Errorf("resource = %+v, want the enrolled server", e.Resource)
	}
	if e.Detail["agent_version"] != "v1.2.0" {
		t.Errorf("detail = %+v, want the agent version", e.Detail)
	}
	// The join token authorised this and must never appear in the record: it is
	// single-use, and an audit row is not where one gets a second life.
	for k, v := range e.Detail {
		if s, ok := v.(string); ok && (s == "jt_1.s3cret" || s == "s3cret") {
			t.Fatalf("the join token reached the audit entry under %q", k)
		}
	}
}

// A refused enrollment records nothing: there is no server, and a row per
// rejected token would let anyone with the endpoint fill the log.
func TestRefusedEnrollmentRecordsNothing(t *testing.T) {
	rec := &recordingAudit{}
	srv := enrollableServer(t, rec)

	if _, err := srv.Enroll(context.Background(), &agentv1.EnrollRequest{
		JoinToken: "jt_1.wrong", CsrPem: csr(t), Hostname: "box-new",
	}); status.Code(err) != codes.PermissionDenied {
		t.Fatalf("Enroll with a bad token = %v, want PermissionDenied", err)
	}
	if len(rec.entries) != 0 {
		t.Fatalf("a refused enrollment recorded %d entries", len(rec.entries))
	}
}

// An audit failure must not cost the agent its certificate: it is already
// enrolled by the time the row is written.
func TestEnrollmentSucceedsWhenTheAuditWriteFails(t *testing.T) {
	rec := &recordingAudit{err: errors.New("boom")}
	srv := enrollableServer(t, rec)

	resp, err := srv.Enroll(context.Background(), &agentv1.EnrollRequest{
		JoinToken: "jt_1.s3cret", CsrPem: csr(t), Hostname: "box-new",
	})
	if err != nil {
		t.Fatalf("Enroll = %v, want the enrollment to stand", err)
	}
	if len(resp.GetCertificatePem()) == 0 {
		t.Fatal("no certificate was issued")
	}
}

// A panel wired without the audit log enrolls exactly as before.
func TestEnrollmentWorksWithoutAnAuditRecorder(t *testing.T) {
	srv := enrollableServer(t, nil)
	if _, err := srv.Enroll(context.Background(), &agentv1.EnrollRequest{
		JoinToken: "jt_1.s3cret", CsrPem: csr(t), Hostname: "box-new",
	}); err != nil {
		t.Fatalf("Enroll without a recorder = %v", err)
	}
}
