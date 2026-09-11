package enroll

import (
	"context"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"testing"
	"time"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
	"github.com/MaramHarsha/cypherpanel/pkg/ids"
	"github.com/MaramHarsha/cypherpanel/pkg/pki"
)

// fakeStore mimics the atomic single-use behavior of the real Postgres store.
type fakeStore struct {
	tokens      map[string]*domain.JoinToken
	enrolled    map[string]bool
	servers     map[string]domain.Server
	enrolledErr error
	now         func() time.Time
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{
		tokens:   map[string]*domain.JoinToken{},
		enrolled: map[string]bool{},
		servers:  map[string]domain.Server{},
		now:      now,
	}
}

func (f *fakeStore) put(id, serverID, secret string, expires time.Time) {
	f.tokens[id] = &domain.JoinToken{
		ID:        id,
		ServerID:  serverID,
		TokenHash: auth.HashToken(secret),
		ExpiresAt: expires,
	}
}

func (f *fakeStore) GetJoinToken(_ context.Context, id string) (domain.JoinToken, error) {
	t, ok := f.tokens[id]
	if !ok {
		return domain.JoinToken{}, store.ErrNotFound
	}
	return *t, nil
}

func (f *fakeStore) ConsumeJoinToken(_ context.Context, id string) (domain.JoinToken, error) {
	t, ok := f.tokens[id]
	if !ok || t.ConsumedAt != nil || !f.now().Before(t.ExpiresAt) {
		return domain.JoinToken{}, store.ErrNotFound
	}
	now := f.now()
	t.ConsumedAt = &now
	return *t, nil
}

func (f *fakeStore) MarkServerEnrolled(_ context.Context, id, hostname, agentVersion string) (domain.Server, error) {
	f.enrolled[id] = true
	if f.servers == nil {
		f.servers = map[string]domain.Server{}
	}
	srv := f.servers[id]
	srv.ID, srv.Hostname, srv.AgentVersion = id, hostname, agentVersion
	f.servers[id] = srv
	return srv, nil
}

func (f *fakeStore) AgentEnrolled(_ context.Context, id string) (bool, error) {
	if f.enrolledErr != nil {
		return false, f.enrolledErr
	}
	return f.enrolled[id], nil
}

func (f *fakeStore) GetServer(_ context.Context, id string) (domain.Server, error) {
	srv, ok := f.servers[id]
	if !ok {
		return domain.Server{}, store.ErrNotFound
	}
	return srv, nil
}

func newServiceWithToken(t *testing.T, expires time.Time, now func() time.Time) (*Service, *fakeStore, string) {
	t.Helper()
	ca, err := pki.NewCA(now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	fs := newFakeStore(now)
	tokenID := ids.New(ids.PrefixJoinToken)
	secret := ids.Secret()
	fs.put(tokenID, "srv_target", secret, expires)

	svc := NewService(fs, ca, time.Hour, "tls://plane:4222")
	svc.now = now
	return svc, fs, FormatToken(tokenID, secret)
}

func freshCSR(t *testing.T) []byte {
	t.Helper()
	_, csrPEM, err := pki.GenerateAgentKey("host")
	if err != nil {
		t.Fatalf("GenerateAgentKey: %v", err)
	}
	return csrPEM
}

func TestEnrollSuccess(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, fs, token := newServiceWithToken(t, now().Add(time.Hour), now)

	res, err := svc.Enroll(context.Background(), token, freshCSR(t), "host1", "0.1.0")
	if err != nil {
		t.Fatalf("Enroll: %v", err)
	}
	if res.ServerID != "srv_target" {
		t.Errorf("ServerID = %q", res.ServerID)
	}
	if !fs.enrolled["srv_target"] {
		t.Error("server not marked enrolled")
	}
	// The issued cert's CN is the server ID, set by the plane.
	block, _ := pem.Decode(res.CertPEM)
	if block == nil {
		t.Fatal("no cert PEM returned")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("ParseCertificate: %v", err)
	}
	if cert.Subject.CommonName != "srv_target" {
		t.Errorf("cert CN = %q, want srv_target", cert.Subject.CommonName)
	}
}

func TestEnrollIsSingleUse(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, _, token := newServiceWithToken(t, now().Add(time.Hour), now)

	if _, err := svc.Enroll(context.Background(), token, freshCSR(t), "h", "v"); err != nil {
		t.Fatalf("first enroll: %v", err)
	}
	if _, err := svc.Enroll(context.Background(), token, freshCSR(t), "h", "v"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("second enroll err = %v, want ErrInvalidToken", err)
	}
}

func TestEnrollWrongSecretDoesNotBurnToken(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, fs, token := newServiceWithToken(t, now().Add(time.Hour), now)
	id, _, _ := splitToken(token)

	badToken := FormatToken(id, "not-the-secret")
	if _, err := svc.Enroll(context.Background(), badToken, freshCSR(t), "h", "v"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("wrong secret err = %v, want ErrInvalidToken", err)
	}
	if fs.tokens[id].ConsumedAt != nil {
		t.Fatal("a wrong-secret attempt must not consume the token")
	}
	// The real secret still works afterward.
	if _, err := svc.Enroll(context.Background(), token, freshCSR(t), "h", "v"); err != nil {
		t.Fatalf("valid enroll after bad attempt: %v", err)
	}
}

func TestEnrollExpiredToken(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, _, token := newServiceWithToken(t, now().Add(-time.Second), now) // already expired
	if _, err := svc.Enroll(context.Background(), token, freshCSR(t), "h", "v"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("expired token err = %v, want ErrInvalidToken", err)
	}
}

func TestEnrollMalformedToken(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, _, _ := newServiceWithToken(t, now().Add(time.Hour), now)
	for _, bad := range []string{"", "no-dot", ".", "id.", ".secret"} {
		if _, err := svc.Enroll(context.Background(), bad, freshCSR(t), "h", "v"); !errors.Is(err, ErrInvalidToken) {
			t.Errorf("token %q: err = %v, want ErrInvalidToken", bad, err)
		}
	}
}

func TestEnrollUnknownToken(t *testing.T) {
	now := func() time.Time { return time.Unix(1_700_000_000, 0) }
	svc, _, _ := newServiceWithToken(t, now().Add(time.Hour), now)
	if _, err := svc.Enroll(context.Background(), FormatToken("jt_nope", "secret"), freshCSR(t), "h", "v"); !errors.Is(err, ErrInvalidToken) {
		t.Fatalf("unknown token err = %v, want ErrInvalidToken", err)
	}
}

// ─── Renewal (agent-identity-and-tls.md §3) ─────────────────────────────────

// enrolledService returns a service whose CA is real and whose store already
// holds one enrolled server, ready to renew.
func enrolledService(t *testing.T, now func() time.Time, ttl time.Duration) (*Service, *fakeStore) {
	t.Helper()
	ca, err := pki.NewCA(now())
	if err != nil {
		t.Fatalf("NewCA: %v", err)
	}
	fs := newFakeStore(now)
	if _, err := fs.MarkServerEnrolled(context.Background(), "srv_target", "box-1", "v1.0.0"); err != nil {
		t.Fatalf("MarkServerEnrolled: %v", err)
	}
	svc := NewService(fs, ca, ttl, "tls://plane:4222")
	svc.now = now
	return svc, fs
}

func TestRenewReissuesTheSameIdentity(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, fs := enrolledService(t, now, 90*24*time.Hour)

	got, err := svc.Renew(context.Background(), "srv_target", "srv_target", freshCSR(t), "v1.1.0")
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if got.ServerID != "srv_target" {
		t.Fatalf("ServerID = %q, want srv_target", got.ServerID)
	}
	block, _ := pem.Decode(got.CertPEM)
	if block == nil {
		t.Fatal("renewal returned no certificate PEM")
	}
	cert, err := x509.ParseCertificate(block.Bytes)
	if err != nil {
		t.Fatalf("parsing renewed cert: %v", err)
	}
	// The identity is unchanged: same CommonName, still a client cert.
	if cert.Subject.CommonName != "srv_target" {
		t.Fatalf("CommonName = %q, want srv_target", cert.Subject.CommonName)
	}
	if len(cert.ExtKeyUsage) != 1 || cert.ExtKeyUsage[0] != x509.ExtKeyUsageClientAuth {
		t.Fatalf("ExtKeyUsage = %v, want client auth only", cert.ExtKeyUsage)
	}
	// The expiry moved forward by the configured TTL.
	if want := now().Add(90 * 24 * time.Hour); !cert.NotAfter.Equal(want) {
		t.Fatalf("NotAfter = %s, want %s", cert.NotAfter, want)
	}
	if !got.NotAfter.Equal(cert.NotAfter) {
		t.Fatalf("reported NotAfter %s disagrees with the certificate %s", got.NotAfter, cert.NotAfter)
	}
	if len(got.CACertPEM) == 0 {
		t.Fatal("renewal returned no CA PEM")
	}
	// A renewal writes NOTHING to the server row. Reusing MarkServerEnrolled
	// would re-stamp enrolled_at, and the panel renders that as
	// "Enrolled <relative time>" — a two-year-old server would claim it joined
	// this morning every sixty days.
	if srv := fs.servers["srv_target"]; srv.Hostname != "box-1" || srv.AgentVersion != "v1.0.0" {
		t.Fatalf("renewal mutated the server row: %+v", srv)
	}
}

func TestRenewRefusesRevokedIdentity(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, fs := enrolledService(t, now, time.Hour)

	// Revocation, as the panel performs it: the server row goes away.
	delete(fs.enrolled, "srv_target")
	delete(fs.servers, "srv_target")

	if _, err := svc.Renew(context.Background(), "srv_target", "srv_target", freshCSR(t), ""); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("Renew after revocation = %v, want ErrUnknownIdentity", err)
	}
}

func TestRenewRefusesUnknownIdentity(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, _ := enrolledService(t, now, time.Hour)

	if _, err := svc.Renew(context.Background(), "srv_never_seen", "srv_never_seen", freshCSR(t), ""); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("Renew for an unknown id = %v, want ErrUnknownIdentity", err)
	}
	// No verified certificate at all is the same refusal, not a panic.
	if _, err := svc.Renew(context.Background(), "", "srv_target", freshCSR(t), ""); !errors.Is(err, ErrUnknownIdentity) {
		t.Fatalf("Renew with no caller identity = %v, want ErrUnknownIdentity", err)
	}
}

// An agent may renew itself and nothing else: a request naming another server
// is refused even though the caller's own certificate is perfectly valid.
func TestRenewRefusesIdentityMismatch(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, fs := enrolledService(t, now, time.Hour)
	if _, err := fs.MarkServerEnrolled(context.Background(), "srv_victim", "box-2", "v1.0.0"); err != nil {
		t.Fatalf("MarkServerEnrolled: %v", err)
	}

	if _, err := svc.Renew(context.Background(), "srv_target", "srv_victim", freshCSR(t), ""); !errors.Is(err, ErrIdentityMismatch) {
		t.Fatalf("Renew across identities = %v, want ErrIdentityMismatch", err)
	}
}

// An empty claim is not a mismatch: older agents may not send one, and the
// caller's certificate is what authorizes the call anyway.
func TestRenewAcceptsAnEmptyClaim(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, _ := enrolledService(t, now, time.Hour)

	got, err := svc.Renew(context.Background(), "srv_target", "", freshCSR(t), "")
	if err != nil {
		t.Fatalf("Renew: %v", err)
	}
	if got.ServerID != "srv_target" {
		t.Fatalf("ServerID = %q, want srv_target", got.ServerID)
	}
}

func TestRenewRejectsAMalformedCSR(t *testing.T) {
	now := func() time.Time { return time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC) }
	svc, _ := enrolledService(t, now, time.Hour)

	if _, err := svc.Renew(context.Background(), "srv_target", "srv_target", []byte("not a csr"), ""); err == nil {
		t.Fatal("Renew accepted a malformed CSR")
	}
}
