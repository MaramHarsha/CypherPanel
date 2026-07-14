package auth

import "testing"

func TestOwnerFilter(t *testing.T) {
	admin := &Claims{Role: RoleRootAdmin}
	admin.Subject = "admin-1"
	if OwnerFilter(admin) != "" {
		t.Fatal("root admin must have no owner filter")
	}

	reseller := &Claims{Role: RoleReseller}
	reseller.Subject = "res-9"
	if OwnerFilter(reseller) != "res-9" {
		t.Fatalf("reseller filter = %q, want res-9", OwnerFilter(reseller))
	}
}

func TestCanAct(t *testing.T) {
	admin := &Claims{Role: RoleRootAdmin}
	admin.Subject = "admin-1"
	reseller := &Claims{Role: RoleReseller}
	reseller.Subject = "res-9"

	if !CanAct(admin, "anyone") {
		t.Fatal("root admin must act on any resource")
	}
	if !CanAct(reseller, "res-9") {
		t.Fatal("reseller must act on own resource")
	}
	if CanAct(reseller, "res-8") {
		t.Fatal("reseller must NOT act on another reseller's resource (IDOR)")
	}
	if CanAct(nil, "x") {
		t.Fatal("nil claims must never be allowed")
	}
}
