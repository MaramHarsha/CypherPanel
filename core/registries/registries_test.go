package registries

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

// fakeStore is the persistence these tests drive the service against.
type fakeStore struct {
	byID    map[string]domain.Registry
	uses    map[string][]domain.RegistryUse
	deleted []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{byID: map[string]domain.Registry{}, uses: map[string][]domain.RegistryUse{}}
}

func (f *fakeStore) CreateRegistry(_ context.Context, r domain.Registry) (domain.Registry, error) {
	f.byID[r.ID] = r
	return r, nil
}

func (f *fakeStore) GetRegistry(_ context.Context, id string) (domain.Registry, error) {
	r, ok := f.byID[id]
	if !ok {
		return domain.Registry{}, store.ErrNotFound
	}
	return r, nil
}

func (f *fakeStore) ListRegistriesByTeams(_ context.Context, teamIDs []string) ([]domain.Registry, error) {
	want := map[string]bool{}
	for _, t := range teamIDs {
		want[t] = true
	}
	var out []domain.Registry
	for _, r := range f.byID {
		if want[r.TeamID] {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *fakeStore) UpdateRegistry(_ context.Context, id string, u store.UpdateRegistryFields) (domain.Registry, error) {
	r, ok := f.byID[id]
	if !ok {
		return domain.Registry{}, store.ErrNotFound
	}
	if u.Name != nil {
		r.Name = *u.Name
	}
	if u.URL != nil {
		r.URL = *u.URL
	}
	if u.Username != nil {
		r.Username = *u.Username
	}
	if u.TokenCT != nil {
		r.TokenCT, r.TokenNonce = u.TokenCT, u.TokenNonce
	}
	if u.CanPull != nil {
		r.CanPull = *u.CanPull
	}
	if u.CanPush != nil {
		r.CanPush = *u.CanPush
	}
	f.byID[id] = r
	return r, nil
}

func (f *fakeStore) RecordRegistryTest(_ context.Context, id string, ok bool, detail string) (domain.Registry, error) {
	r := f.byID[id]
	r.LastTestOK, r.LastTestDetail = ok, detail
	f.byID[id] = r
	return r, nil
}

func (f *fakeStore) DeleteRegistry(_ context.Context, id string) error {
	f.deleted = append(f.deleted, id)
	delete(f.byID, id)
	return nil
}

func (f *fakeStore) ApplicationsUsingRegistry(_ context.Context, id string) ([]domain.RegistryUse, error) {
	return f.uses[id], nil
}

// fakeBox is a reversible stand-in for the master-key box: the sealed form is
// the plaintext with a marker, so a test can assert the token was resealed (or
// left alone) without reaching into crypto.
type fakeBox struct{ seals int }

func (b *fakeBox) Seal(pt []byte) ([]byte, []byte, error) {
	b.seals++
	return append([]byte("sealed:"), pt...), []byte("nonce"), nil
}

func (b *fakeBox) Open(ct, _ []byte) ([]byte, error) {
	return []byte(strings.TrimPrefix(string(ct), "sealed:")), nil
}

func newService() (*Service, *fakeStore, *fakeBox) {
	st, box := newFakeStore(), &fakeBox{}
	return NewService(st, box), st, box
}

func TestCreateSealsTheTokenAndKeepsItOutOfTheRecord(t *testing.T) {
	svc, _, box := newService()
	reg, err := svc.Create(context.Background(), "team_1", Input{
		Name: "ghcr", URL: "ghcr.io", Username: "acme", Token: "s3cret", CanPull: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if box.seals != 1 {
		t.Fatalf("seals = %d, want the token sealed exactly once", box.seals)
	}
	if strings.Contains(reg.Name+reg.URL+reg.Username, "s3cret") {
		t.Fatal("the token leaked into a plain field of the record")
	}
	if !strings.HasPrefix(reg.ID, "reg_") {
		t.Fatalf("id = %q, want the reg_ prefix", reg.ID)
	}
}

func TestCreateRejectsInputTheDeployPathCouldNotUse(t *testing.T) {
	svc, _, _ := newService()
	cases := []struct {
		name string
		in   Input
	}{
		{"no name", Input{URL: "ghcr.io", Token: "t", CanPull: true}},
		{"no url", Input{Name: "n", Token: "t", CanPull: true}},
		{"url carries a scheme", Input{Name: "n", URL: "https://ghcr.io", Token: "t", CanPull: true}},
		{"url has internal whitespace", Input{Name: "n", URL: "ghcr.io registry", Token: "t", CanPull: true}},
		{"no token", Input{Name: "n", URL: "ghcr.io", CanPull: true}},
		{"neither pull nor push", Input{Name: "n", URL: "ghcr.io", Token: "t"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := svc.Create(context.Background(), "team_1", tc.in)
			var ve *ValidationError
			if !errors.As(err, &ve) {
				t.Fatalf("err = %v, want a ValidationError", err)
			}
		})
	}
}

// A URL with a scheme is the one that matters most: it would be stored, then
// concatenated into an image reference nothing can pull.
func TestCreateTrimsSurroundingWhitespace(t *testing.T) {
	svc, _, _ := newService()
	reg, err := svc.Create(context.Background(), "team_1", Input{
		Name: "  ghcr  ", URL: "  ghcr.io  ", Username: "  acme  ", Token: "t", CanPull: true,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if reg.Name != "ghcr" || reg.URL != "ghcr.io" || reg.Username != "acme" {
		t.Fatalf("got %+v, want the fields trimmed", reg)
	}
}

func TestUpdateWithoutATokenKeepsTheStoredCiphertext(t *testing.T) {
	svc, st, box := newService()
	reg, err := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Token: "s3cret", CanPull: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	before := box.seals

	name := "ghcr-prod"
	updated, err := svc.Update(context.Background(), reg.ID, UpdateInput{Name: &name})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if box.seals != before {
		t.Fatalf("seals = %d, want no reseal when no token was sent", box.seals)
	}
	if string(updated.TokenCT) != string(reg.TokenCT) {
		t.Fatal("the ciphertext changed on an update that sent no token")
	}
	if st.byID[reg.ID].Name != "ghcr-prod" {
		t.Fatalf("name = %q, want the rename applied", st.byID[reg.ID].Name)
	}
}

func TestUpdateRotatesTheTokenWhenOneIsSent(t *testing.T) {
	svc, _, box := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Token: "old", CanPull: true})
	before := box.seals

	next := "new"
	updated, err := svc.Update(context.Background(), reg.ID, UpdateInput{Token: &next})
	if err != nil {
		t.Fatalf("Update: %v", err)
	}
	if box.seals != before+1 {
		t.Fatalf("seals = %d, want exactly one reseal", box.seals)
	}
	if !strings.HasSuffix(string(updated.TokenCT), "new") {
		t.Fatalf("ciphertext = %q, want the new token sealed", updated.TokenCT)
	}
}

// A PATCH must not be able to leave a registry in a state Create would have
// refused — here, one allowed to do nothing at all.
func TestUpdateHoldsTheMergedResultToTheCreateRules(t *testing.T) {
	svc, _, _ := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Token: "t", CanPull: true})

	no := false
	_, err := svc.Update(context.Background(), reg.ID, UpdateInput{CanPull: &no})
	var ve *ValidationError
	if !errors.As(err, &ve) {
		t.Fatalf("err = %v, want clearing the last capability refused", err)
	}
}

func TestDeleteRefusesWhileApplicationsUseIt(t *testing.T) {
	svc, st, _ := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Token: "t", CanPull: true})
	st.uses[reg.ID] = []domain.RegistryUse{{ApplicationID: "app_1", ApplicationName: "web", Pulls: true}}

	if err := svc.Delete(context.Background(), reg.ID); !errors.Is(err, ErrInUse) {
		t.Fatalf("err = %v, want ErrInUse", err)
	}
	if len(st.deleted) != 0 {
		t.Fatal("the registry was deleted despite being in use")
	}
}

func TestDeleteRemovesAnUnusedRegistry(t *testing.T) {
	svc, st, _ := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Token: "t", CanPull: true})

	if err := svc.Delete(context.Background(), reg.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if len(st.deleted) != 1 || st.deleted[0] != reg.ID {
		t.Fatalf("deleted = %v, want just %s", st.deleted, reg.ID)
	}
}

func TestCredentialUnsealsTheToken(t *testing.T) {
	svc, _, _ := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{Name: "ghcr", URL: "ghcr.io", Username: "acme", Token: "s3cret", CanPull: true})

	cred, err := svc.Credential(context.Background(), reg.ID)
	if err != nil {
		t.Fatalf("Credential: %v", err)
	}
	if cred.Token != "s3cret" || cred.Username != "acme" || cred.URL != "ghcr.io" {
		t.Fatalf("cred = %+v", cred)
	}
}

// probe speaks the OCI distribution spec's own auth endpoint. The three answers
// it must tell apart are "authenticated", "rejected" and "something else" —
// conflating the last two is how a working credential looks broken.
func TestProbeDistinguishesTheRegistrysThreeAnswers(t *testing.T) {
	cases := []struct {
		name     string
		status   int
		wantOK   bool
		wantSaid string
	}{
		{"authenticated", http.StatusOK, true, "Authenticated"},
		{"rejected", http.StatusUnauthorized, false, "rejected these credentials"},
		{"forbidden is also a rejection", http.StatusForbidden, false, "rejected these credentials"},
		{"anything else is reported verbatim", http.StatusBadGateway, false, "502"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/v2/" {
					t.Errorf("path = %q, want /v2/ — the spec's own auth endpoint", r.URL.Path)
				}
				if u, p, ok := r.BasicAuth(); !ok || u != "acme" || p != "s3cret" {
					t.Errorf("basic auth = %q/%q ok=%v, want the credential sent", u, p, ok)
				}
				w.WriteHeader(tc.status)
			}))
			defer srv.Close()

			svc, _, _ := newService()
			res := svc.probe(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "acme", "s3cret")
			if res.OK != tc.wantOK {
				t.Fatalf("ok = %v, want %v (detail %q)", res.OK, tc.wantOK, res.Detail)
			}
			if !strings.Contains(res.Detail, tc.wantSaid) {
				t.Fatalf("detail = %q, want it to mention %q", res.Detail, tc.wantSaid)
			}
		})
	}
}

// A bearer-token registry has no username. Sending an empty one as basic auth
// would authenticate as the user "" rather than with the token.
func TestProbeSendsABearerTokenWhenThereIsNoUsername(t *testing.T) {
	var got string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = r.Header.Get("Authorization")
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, _, _ := newService()
	svc.probe(context.Background(), strings.TrimPrefix(srv.URL, "http://"), "", "tok")
	if got != "Bearer tok" {
		t.Fatalf("Authorization = %q, want a bearer token", got)
	}
}

// The namespace is part of an image path, not of the host that answers /v2/.
func TestProbeAsksTheHostNotTheNamespace(t *testing.T) {
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, _, _ := newService()
	host := strings.TrimPrefix(srv.URL, "http://")
	res := svc.probe(context.Background(), host+"/acme/team", "u", "p")
	if !res.OK || path != "/v2/" {
		t.Fatalf("ok = %v path = %q, want the namespace stripped", res.OK, path)
	}
}

// A failed dial must report what happened without the URL: a *url.Error
// stringifies the whole request URL, and a bearer token can live in one.
func TestProbeDoesNotEchoTheURLOnADialFailure(t *testing.T) {
	svc, _, _ := newService()
	res := svc.probe(context.Background(), "127.0.0.1:1", "u", "p")
	if res.OK {
		t.Fatal("ok = true, want a failure against a closed port")
	}
	if strings.Contains(res.Detail, "http://") || strings.Contains(res.Detail, "/v2/") {
		t.Fatalf("detail = %q, want the URL kept out of it", res.Detail)
	}
}

func TestTestRecordsTheOutcome(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, st, _ := newService()
	reg, _ := svc.Create(context.Background(), "team_1", Input{
		Name: "local", URL: strings.TrimPrefix(srv.URL, "http://"), Token: "t", CanPull: true,
	})
	res, err := svc.Test(context.Background(), reg.ID)
	if err != nil {
		t.Fatalf("Test: %v", err)
	}
	if !res.OK || !st.byID[reg.ID].LastTestOK {
		t.Fatalf("res = %+v stored = %+v, want the success recorded", res, st.byID[reg.ID])
	}
}

// TestConfig proves a credential before anything is stored: a dialog that can
// only test what it already saved teaches operators to save broken ones.
func TestTestConfigStoresNothing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	svc, st, _ := newService()
	res, err := svc.TestConfig(context.Background(), Input{
		Name: "probe", URL: strings.TrimPrefix(srv.URL, "http://"), Username: "u", Token: "p", CanPull: true,
	})
	if err != nil || !res.OK {
		t.Fatalf("res = %+v err = %v", res, err)
	}
	if len(st.byID) != 0 {
		t.Fatalf("stored %d registries, want none", len(st.byID))
	}
}

// A credential must not go on the wire in the clear to a host nobody vouched
// for; localhost is the exception the Docker daemon itself makes.
func TestIsPlainHTTPHostOnlyAcceptsLoopback(t *testing.T) {
	plain := []string{"localhost", "localhost:5000", "127.0.0.1", "127.0.0.1:5000", "127.5.5.5:5000"}
	tls := []string{"ghcr.io", "registry.example.com", "registry:5000", "10.0.0.1:5000", "notlocalhost"}
	for _, h := range plain {
		if !isPlainHTTPHost(h) {
			t.Errorf("isPlainHTTPHost(%q) = false, want true", h)
		}
	}
	for _, h := range tls {
		if isPlainHTTPHost(h) {
			t.Errorf("isPlainHTTPHost(%q) = true, want TLS", h)
		}
	}
}
