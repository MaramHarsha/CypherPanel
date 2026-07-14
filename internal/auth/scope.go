package auth

// Resource scoping helpers (plan.md Section 6). These centralize the
// "what may this caller see / act on" rule so handlers never grow ad-hoc
// `if role == ...` checks — the exact place IDOR bugs otherwise creep in.

// OwnerFilter returns the owner ID a caller's list/read is limited to. A root
// admin is unrestricted (empty string = no filter); a reseller (or end user)
// is limited to resources they own — i.e. their own user ID.
func OwnerFilter(c *Claims) string {
	if c == nil || c.Role == RoleRootAdmin {
		return ""
	}
	return c.Subject
}

// CanAct reports whether the caller may mutate a resource owned by ownerID.
// Root admins may act on anything; everyone else only on resources they own.
// Callers MUST fetch the resource's real owner and pass it here BEFORE acting.
func CanAct(c *Claims, ownerID string) bool {
	if c == nil {
		return false
	}
	return c.Role == RoleRootAdmin || c.Subject == ownerID
}
