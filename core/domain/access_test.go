package domain

// The two pieces of logic the access feature keeps in the domain: who may hand
// out which rank, and what state an invitation is in at a given instant. Both
// are pure and both are load-bearing — one is the only place "who may mint an
// owner" is decided, the other is what a listing prints and what the service
// checks before it even asks the database to spend a row.

import (
	"testing"
	"time"
)

func TestCanGrantRole(t *testing.T) {
	for _, tc := range []struct {
		actor, subject string
		want           bool
	}{
		{RoleOwner, RoleOwner, true},
		{RoleOwner, RoleAdmin, true},
		{RoleOwner, RoleMember, true},
		{RoleAdmin, RoleAdmin, true},
		{RoleAdmin, RoleMember, true},
		// The rule the whole feature rests on: only an owner mints an owner.
		{RoleAdmin, RoleOwner, false},
		{RoleMember, RoleMember, false},
		{RoleMember, RoleAdmin, false},
		{RoleMember, RoleOwner, false},
		// An unknown or empty rank is below member and grants nothing, in
		// either position: a corrupt role must never widen access.
		{"", RoleMember, false},
		{"superuser", RoleMember, false},
		{RoleAdmin, "superuser", true}, // an unknown SUBJECT rank needs only admin, like member
	} {
		if got := CanGrantRole(tc.actor, tc.subject); got != tc.want {
			t.Errorf("CanGrantRole(%q, %q) = %v, want %v", tc.actor, tc.subject, got, tc.want)
		}
	}
}

func TestTeamInviteState(t *testing.T) {
	now := time.Date(2026, 9, 5, 12, 0, 0, 0, time.UTC)
	at := now.Add(-time.Hour)
	live := TeamInvite{ExpiresAt: now.Add(InviteTTL)}

	for _, tc := range []struct {
		name string
		inv  TeamInvite
		want string
	}{
		{"pending", live, InviteStatePending},
		{"expired", TeamInvite{ExpiresAt: now.Add(-time.Second)}, InviteStateExpired},
		// Expiry is the boundary, and it is half-open: an invitation is dead at
		// the instant it expires, not a second later.
		{"expired at the instant", TeamInvite{ExpiresAt: now}, InviteStateExpired},
		{"accepted", TeamInvite{ExpiresAt: now.Add(InviteTTL), AcceptedAt: &at}, InviteStateAccepted},
		{"revoked", TeamInvite{ExpiresAt: now.Add(InviteTTL), RevokedAt: &at}, InviteStateRevoked},
		// Accepted wins over revoked and over expiry: what happened to it is
		// more informative than what would have happened to it.
		{"accepted then expired", TeamInvite{ExpiresAt: now.Add(-time.Hour), AcceptedAt: &at}, InviteStateAccepted},
		{"revoked and expired", TeamInvite{ExpiresAt: now.Add(-time.Hour), RevokedAt: &at}, InviteStateRevoked},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := tc.inv.State(now); got != tc.want {
				t.Errorf("State = %q, want %q", got, tc.want)
			}
			if got, want := tc.inv.Acceptable(now), tc.want == InviteStatePending; got != want {
				t.Errorf("Acceptable = %v, want %v", got, want)
			}
		})
	}
}
