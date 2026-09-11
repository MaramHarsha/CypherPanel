package domain

// The ability vocabulary and the one implication in it (api-tokens.md).
//
// The implication is the whole compatibility story: `write` has to keep
// satisfying the narrow abilities carved out of it, or every token already in
// someone's CI configuration quietly loses capability the day this ships.

import "testing"

func TestAbilityImplication(t *testing.T) {
	cases := []struct {
		held, want Ability
		ok         bool
	}{
		// Every ability satisfies itself.
		{AbilityRead, AbilityRead, true},
		{AbilityWrite, AbilityWrite, true},
		{AbilityDeploy, AbilityDeploy, true},
		{AbilityEnv, AbilityEnv, true},
		{AbilityServers, AbilityServers, true},
		{AbilityAdmin, AbilityAdmin, true},

		// write covers what was carved out of it, so old tokens are unchanged.
		{AbilityWrite, AbilityEnv, true},
		{AbilityWrite, AbilityServers, true},
		{AbilityWrite, AbilityAdmin, true},

		// It does not reach across into the orthogonal ones.
		{AbilityWrite, AbilityRead, false},
		{AbilityWrite, AbilityDeploy, false},

		// A narrow ability never widens into another, which is the point of
		// minting one.
		{AbilityEnv, AbilityWrite, false},
		{AbilityEnv, AbilityServers, false},
		{AbilityEnv, AbilityAdmin, false},
		{AbilityServers, AbilityAdmin, false},
		{AbilityAdmin, AbilityWrite, false},
		{AbilityDeploy, AbilityWrite, false},
		{AbilityRead, AbilityWrite, false},
	}
	for _, c := range cases {
		if got := c.held.Implies(c.want); got != c.ok {
			t.Errorf("Ability(%q).Implies(%q) = %v, want %v", c.held, c.want, got, c.ok)
		}
	}
}

// A token issued before the narrow abilities existed carries exactly
// read/write/deploy. It must still satisfy every requirement it satisfied
// before, including the three new ones.
func TestLegacyTokenSetLosesNothing(t *testing.T) {
	legacy := AllAbilities() // read, write, deploy — what was stored before
	for _, want := range []Ability{AbilityRead, AbilityWrite, AbilityDeploy, AbilityEnv, AbilityServers, AbilityAdmin} {
		if !HasAbility(legacy, want) {
			t.Errorf("a pre-existing token no longer satisfies %q", want)
		}
	}
}

// A narrow set satisfies only what it names.
func TestNarrowSetGrantsOnlyItself(t *testing.T) {
	envOnly := []Ability{AbilityRead, AbilityEnv}
	if !HasAbility(envOnly, AbilityEnv) || !HasAbility(envOnly, AbilityRead) {
		t.Fatal("an env token cannot read or set env vars")
	}
	for _, denied := range []Ability{AbilityWrite, AbilityDeploy, AbilityServers, AbilityAdmin} {
		if HasAbility(envOnly, denied) {
			t.Errorf("an env-only token satisfies %q", denied)
		}
	}
}

func TestValidAbility(t *testing.T) {
	for _, a := range []Ability{AbilityRead, AbilityWrite, AbilityDeploy, AbilityEnv, AbilityServers, AbilityAdmin} {
		if !ValidAbility(a) {
			t.Errorf("ValidAbility(%q) = false", a)
		}
	}
	for _, a := range []Ability{"", "owner", "Write", "read ", "*"} {
		if ValidAbility(a) {
			t.Errorf("ValidAbility(%q) = true, want false", a)
		}
	}
}

// AllAbilities is what a token gets when the caller names none. It must not
// silently include the narrow ones as separate entries: write already covers
// them, and listing both would be the same grant written twice.
func TestAllAbilitiesIsTheLegacyTrio(t *testing.T) {
	got := AllAbilities()
	if len(got) != 3 {
		t.Fatalf("AllAbilities() = %v, want exactly read, write, deploy", got)
	}
	for _, want := range []Ability{AbilityRead, AbilityWrite, AbilityDeploy} {
		found := false
		for _, a := range got {
			if a == want {
				found = true
			}
		}
		if !found {
			t.Errorf("AllAbilities() is missing %q", want)
		}
	}
}
