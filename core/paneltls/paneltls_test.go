package paneltls_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
	"github.com/MaramHarsha/cypherpanel/core/paneltls"
	"github.com/MaramHarsha/cypherpanel/core/store"
)

type fakeStore struct {
	set     *domain.PanelTLS
	deletes int
	getErr  error
}

func (f *fakeStore) GetPanelTLS(context.Context) (domain.PanelTLS, error) {
	if f.getErr != nil {
		return domain.PanelTLS{}, f.getErr
	}
	if f.set == nil {
		return domain.PanelTLS{}, store.ErrNotFound
	}
	return *f.set, nil
}

func (f *fakeStore) SetPanelTLS(_ context.Context, t domain.PanelTLS) error {
	c := t
	f.set = &c
	return nil
}

func (f *fakeStore) DeletePanelTLS(context.Context) error {
	f.deletes++
	f.set = nil
	return nil
}

type fakeFleet struct {
	reasons []string
	err     error
}

func (f *fakeFleet) RequestResync(_ context.Context, reason string) error {
	f.reasons = append(f.reasons, reason)
	return f.err
}

func newService(t *testing.T, st *fakeStore, fl paneltls.Fleet) *paneltls.Service {
	t.Helper()
	return paneltls.NewService(st, fl, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

func TestGetUnsetIsNotAnError(t *testing.T) {
	svc := newService(t, &fakeStore{}, nil)
	got, err := svc.Get(context.Background())
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.Configured() {
		t.Fatalf("fresh panel reports TLS configured: %+v", got)
	}
	ok, err := svc.Configured(context.Background())
	if err != nil || ok {
		t.Fatalf("Configured = %v, %v; want false, nil", ok, err)
	}
}

func TestConfiguredPropagatesReadFailure(t *testing.T) {
	boom := errors.New("database is down")
	svc := newService(t, &fakeStore{getErr: boom}, nil)
	if _, err := svc.Configured(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("Configured error = %v, want it to wrap %v", err, boom)
	}
}

func TestSetStoresValidatesAndNudgesTheFleet(t *testing.T) {
	st := &fakeStore{}
	fleet := &fakeFleet{}
	svc := newService(t, st, fleet)

	got, err := svc.Set(context.Background(), paneltls.Input{
		ACMEEmail:    "  ops@example.com ",
		ACMECAServer: " https://acme-staging-v02.api.letsencrypt.org/directory ",
	})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.ACMEEmail != "ops@example.com" || got.ACMECAServer != "https://acme-staging-v02.api.letsencrypt.org/directory" {
		t.Fatalf("Set = %+v, want trimmed values", got)
	}
	if !got.Configured() {
		t.Fatal("Set result does not report configured")
	}
	if len(fleet.reasons) != 1 {
		t.Fatalf("fleet nudges = %d, want 1", len(fleet.reasons))
	}
}

func TestSetEmptyEmailClears(t *testing.T) {
	st := &fakeStore{set: &domain.PanelTLS{ACMEEmail: "ops@example.com"}}
	fleet := &fakeFleet{}
	svc := newService(t, st, fleet)

	got, err := svc.Set(context.Background(), paneltls.Input{})
	if err != nil {
		t.Fatalf("Set: %v", err)
	}
	if got.Configured() {
		t.Fatalf("clearing returned a configured account: %+v", got)
	}
	if st.deletes != 1 {
		t.Fatalf("deletes = %d, want 1", st.deletes)
	}
	if len(fleet.reasons) != 1 {
		t.Fatalf("clearing did not nudge the fleet: %v", fleet.reasons)
	}
}

func TestSetRejectsBadInput(t *testing.T) {
	tests := []struct {
		name string
		in   paneltls.Input
		want error
	}{
		{"display name", paneltls.Input{ACMEEmail: "Ops <ops@example.com>"}, paneltls.ErrInvalidEmail},
		{"not an address", paneltls.Input{ACMEEmail: "ops-at-example.com"}, paneltls.ErrInvalidEmail},
		{"two addresses", paneltls.Input{ACMEEmail: "a@example.com, b@example.com"}, paneltls.ErrInvalidEmail},
		{"relative ca server", paneltls.Input{ACMEEmail: "ops@example.com", ACMECAServer: "/directory"}, paneltls.ErrInvalidCAServer},
		{"non-http ca server", paneltls.Input{ACMEEmail: "ops@example.com", ACMECAServer: "ftp://acme.example.com/d"}, paneltls.ErrInvalidCAServer},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			st := &fakeStore{}
			svc := newService(t, st, nil)
			if _, err := svc.Set(context.Background(), tc.in); !errors.Is(err, tc.want) {
				t.Fatalf("Set error = %v, want %v", err, tc.want)
			}
			if st.set != nil {
				t.Fatalf("invalid input was stored: %+v", st.set)
			}
		})
	}
}

// A fleet that cannot be reached must not fail the write: the settings are
// already durable, and every agent re-reads desired state on its next connect.
func TestSetSucceedsWhenTheFleetNudgeFails(t *testing.T) {
	st := &fakeStore{}
	svc := newService(t, st, &fakeFleet{err: errors.New("bus down")})
	if _, err := svc.Set(context.Background(), paneltls.Input{ACMEEmail: "ops@example.com"}); err != nil {
		t.Fatalf("Set: %v", err)
	}
	if st.set == nil {
		t.Fatal("settings were not stored")
	}
}

func TestRouteTLSState(t *testing.T) {
	tests := []struct {
		name  string
		route domain.AppRoute
		acme  bool
		want  string
	}{
		{"no domain", domain.AppRoute{}, true, ""},
		{"https with resolver", domain.AppRoute{Domain: "app.example.com", HTTPS: true}, true, domain.TLSStateHTTPS},
		{"https without resolver", domain.AppRoute{Domain: "app.example.com", HTTPS: true}, false, domain.TLSStateHTTPOnlyNoResolver},
		{"http by choice", domain.AppRoute{Domain: "app.example.com"}, true, domain.TLSStateHTTPOnly},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := domain.RouteTLSState(tc.route, tc.acme); got != tc.want {
				t.Fatalf("RouteTLSState = %q, want %q", got, tc.want)
			}
		})
	}
}
