package applications

// Front-door access control (app-access-control.md). Two named capabilities
// rather than a middleware escape hatch: the panel can validate a CIDR, hash a
// passphrase, audit the change and describe the result honestly, none of which
// it could do for a pass-through block.

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"strings"

	"github.com/MaramHarsha/cypherpanel/core/auth"
	"github.com/MaramHarsha/cypherpanel/core/domain"
)

// ErrInvalidCIDR is returned with the offending entry named, so the operator
// fixes the one that is wrong rather than re-reading the whole list.
var ErrInvalidCIDR = errors.New("not a valid CIDR")

// ErrAllowlistEmpty refuses an enabled allowlist with nothing in it. An empty
// allowlist that means "allow nothing" is a lockout nobody typed, and one that
// means "allow everything" is a control that silently does not apply — so the
// only honest answer is to refuse the state.
var ErrAllowlistEmpty = errors.New("an enabled allowlist needs at least one CIDR")

// ErrPasswordTooShort keeps a preview passphrase above a floor. It guards a
// staging site from the open internet, not a bank, but four characters is not a
// guard.
var ErrPasswordTooShort = errors.New("a preview passphrase needs at least 8 characters")

// SetAllowlist replaces the allowlist wholesale. Normalizing through netip is
// what makes "203.0.113.5/24" — a host address with a network prefix, which
// Traefik silently reinterprets — either corrected or refused here rather than
// behaving surprisingly on the node.
func (s *Service) SetAllowlist(ctx context.Context, id string, enabled bool, cidrs []string) (domain.Application, error) {
	clean := make([]string, 0, len(cidrs))
	seen := map[string]bool{}
	for _, raw := range cidrs {
		e := strings.TrimSpace(raw)
		if e == "" {
			continue
		}
		// A bare address is accepted and read as a single host, because that is
		// what an operator means by "198.51.100.7" and refusing it would be
		// pedantry with a support cost.
		if !strings.Contains(e, "/") {
			addr, err := netip.ParseAddr(e)
			if err != nil {
				return domain.Application{}, fmt.Errorf("%w: %q", ErrInvalidCIDR, raw)
			}
			e = fmt.Sprintf("%s/%d", addr.String(), addr.BitLen())
		}
		pfx, err := netip.ParsePrefix(e)
		if err != nil {
			return domain.Application{}, fmt.Errorf("%w: %q", ErrInvalidCIDR, raw)
		}
		// Masked, so the stored value is the network the operator actually
		// described rather than a host address that looks like one.
		norm := pfx.Masked().String()
		if seen[norm] {
			continue
		}
		seen[norm] = true
		clean = append(clean, norm)
	}
	if enabled && len(clean) == 0 {
		return domain.Application{}, ErrAllowlistEmpty
	}
	return s.store.SetApplicationAllowlist(ctx, id, enabled, clean)
}

// SetPreviewPassword hashes the passphrase and stores only the hash. The
// plaintext exists in the operator's clipboard and nowhere else; the caller
// returns it exactly once and the API never reads it back — the contract
// reset-password already has, for the reason rule 20 gives.
func (s *Service) SetPreviewPassword(ctx context.Context, id, passphrase string) (domain.Application, error) {
	if len([]rune(passphrase)) < 8 {
		return domain.Application{}, ErrPasswordTooShort
	}
	hash, err := auth.HashPassword(passphrase)
	if err != nil {
		return domain.Application{}, fmt.Errorf("applications: hashing preview passphrase: %w", err)
	}
	return s.store.SetApplicationPreviewPassword(ctx, id, true, hash)
}

// SetMaintenance turns the holding page on or off. There is no validation to do
// and no auto-expiry to offer: a window that lifts itself while the migration is
// still running publishes a half-migrated application to the internet, which is
// a worse Monday than the one it prevents (app-access-control.md §10).
func (s *Service) SetMaintenance(ctx context.Context, id string, on bool) (domain.Application, error) {
	return s.store.SetApplicationMaintenance(ctx, id, on)
}

// ClearPreviewPassword turns the gate off and forgets the hash. Turning it off
// without forgetting would leave a credential nobody can see and nobody can
// rotate.
func (s *Service) ClearPreviewPassword(ctx context.Context, id string) (domain.Application, error) {
	return s.store.SetApplicationPreviewPassword(ctx, id, false, "")
}
