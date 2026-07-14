package services

import (
	"reflect"
	"testing"
)

func TestParseShow(t *testing.T) {
	cases := []struct {
		name          string
		out           string
		wantState     string
		wantInstalled bool
	}{
		{"active", "LoadState=loaded\nActiveState=active\n", "active", true},
		{"failed", "LoadState=loaded\nActiveState=failed\n", "failed", true},
		{"inactive", "LoadState=loaded\nActiveState=inactive\n", "inactive", true},
		{"not installed", "LoadState=not-found\nActiveState=inactive\n", "", false},
		{"reversed order", "ActiveState=active\nLoadState=loaded", "active", true},
		{"garbage", "no equals here", "unknown", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			state, installed := parseShow(c.out)
			if state != c.wantState || installed != c.wantInstalled {
				t.Fatalf("parseShow = (%q, %v), want (%q, %v)", state, installed, c.wantState, c.wantInstalled)
			}
		})
	}
}

func TestManagedServicesDefault(t *testing.T) {
	t.Setenv("CYPHER_MANAGED_SERVICES", "")
	if len(ManagedServices()) == 0 {
		t.Fatal("default managed services must not be empty")
	}
}

func TestManagedServicesOverride(t *testing.T) {
	t.Setenv("CYPHER_MANAGED_SERVICES", "nginx, custom-svc ,")
	got := ManagedServices()
	want := []string{"nginx", "custom-svc"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ManagedServices override = %v, want %v", got, want)
	}
}
