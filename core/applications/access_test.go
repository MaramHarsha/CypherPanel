package applications

import (
	"context"
	"errors"
	"testing"

	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// withApp seeds one application so the access calls have something to act on.
func withApp() *fakeStore {
	f := newFakeStore()
	f.apps["app1"] = domain.Application{ID: "app1", EnvironmentID: "env_1", Name: "web"}
	return f
}

// Normalization is the point: Traefik reinterprets a host address carrying a
// network prefix, so "203.0.113.5/24" must become the network the operator
// described rather than behave surprisingly on the node.
func TestAllowlistIsNormalized(t *testing.T) {
	s := NewService(withApp(), nil)
	app, err := s.SetAllowlist(context.Background(), "app1", true,
		[]string{"203.0.113.5/24", " 198.51.100.7 ", "203.0.113.9/24"})
	if err != nil {
		t.Fatalf("SetAllowlist: %v", err)
	}
	got := app.Access.IPAllowlist
	want := []string{"203.0.113.0/24", "198.51.100.7/32"}
	if len(got) != len(want) {
		t.Fatalf("allowlist = %v, want %v (the two /24s are the same network and collapse)", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("entry %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// An enabled allowlist with nothing in it is refused rather than guessed at:
// empty meaning "allow nothing" is a lockout nobody typed, and empty meaning
// "allow everything" is a control that silently does not apply.
func TestEnabledAllowlistCannotBeEmpty(t *testing.T) {
	s := NewService(withApp(), nil)
	if _, err := s.SetAllowlist(context.Background(), "app1", true, nil); !errors.Is(err, ErrAllowlistEmpty) {
		t.Fatalf("err = %v, want ErrAllowlistEmpty", err)
	}
	// Disabled with no entries is fine — that is simply "off".
	if _, err := s.SetAllowlist(context.Background(), "app1", false, nil); err != nil {
		t.Fatalf("disabling with an empty list: %v", err)
	}
}

func TestInvalidCIDRNamesTheOffendingEntry(t *testing.T) {
	s := NewService(withApp(), nil)
	_, err := s.SetAllowlist(context.Background(), "app1", true, []string{"203.0.113.0/24", "not-an-address"})
	if !errors.Is(err, ErrInvalidCIDR) {
		t.Fatalf("err = %v, want ErrInvalidCIDR", err)
	}
	if !contains(err.Error(), "not-an-address") {
		t.Errorf("error %q does not name the offending entry", err)
	}
}

// The passphrase is hashed and never stored in the clear.
func TestPreviewPassphraseIsHashed(t *testing.T) {
	s := NewService(withApp(), nil)
	app, err := s.SetPreviewPassword(context.Background(), "app1", "correct horse battery")
	if err != nil {
		t.Fatalf("SetPreviewPassword: %v", err)
	}
	h := app.Access.PreviewPasswordHash
	if h == "" || h == "correct horse battery" {
		t.Fatalf("hash = %q — the passphrase must never be stored in the clear", h)
	}
	if !app.Access.PreviewPasswordEnabled {
		t.Error("setting a passphrase did not enable the gate")
	}
	// Clearing forgets it: leaving the hash would keep a credential nobody can
	// see and nobody can rotate.
	cleared, err := s.ClearPreviewPassword(context.Background(), "app1")
	if err != nil {
		t.Fatalf("ClearPreviewPassword: %v", err)
	}
	if cleared.Access.PreviewPasswordHash != "" || cleared.Access.PreviewPasswordEnabled {
		t.Errorf("clearing left hash=%q enabled=%v", cleared.Access.PreviewPasswordHash, cleared.Access.PreviewPasswordEnabled)
	}
}

func TestShortPassphraseIsRefused(t *testing.T) {
	s := NewService(withApp(), nil)
	if _, err := s.SetPreviewPassword(context.Background(), "app1", "short"); !errors.Is(err, ErrPasswordTooShort) {
		t.Fatalf("err = %v, want ErrPasswordTooShort", err)
	}
}

func contains(hay, needle string) bool {
	return len(hay) >= len(needle) && (hay == needle || len(needle) == 0 || indexOf(hay, needle) >= 0)
}

func indexOf(hay, needle string) int {
	for i := 0; i+len(needle) <= len(hay); i++ {
		if hay[i:i+len(needle)] == needle {
			return i
		}
	}
	return -1
}
