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
	tokens   map[string]*domain.JoinToken
	enrolled map[string]bool
	now      func() time.Time
}

func newFakeStore(now func() time.Time) *fakeStore {
	return &fakeStore{tokens: map[string]*domain.JoinToken{}, enrolled: map[string]bool{}, now: now}
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

func (f *fakeStore) MarkServerEnrolled(_ context.Context, id, _, _ string) (domain.Server, error) {
	f.enrolled[id] = true
	return domain.Server{ID: id}, nil
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
