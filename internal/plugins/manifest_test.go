package plugins

import "testing"

func validManifest() Manifest {
	return Manifest{
		APIVersion:  ManifestAPIVersion,
		Name:        "hello-world",
		Version:     "1.0.0",
		Kind:        KindPlugin,
		Events:      []string{"events.account.created"},
		Permissions: []string{"accounts:read"},
	}
}

func TestValidateAcceptsGoodManifest(t *testing.T) {
	m := validManifest()
	if err := m.Validate(); err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
}

func TestValidateDefaultsKind(t *testing.T) {
	m := validManifest()
	m.Kind = ""
	if err := m.Validate(); err != nil {
		t.Fatalf("empty kind should default, got: %v", err)
	}
	if m.Kind != KindPlugin {
		t.Fatalf("kind not defaulted to plugin: %q", m.Kind)
	}
}

func TestValidateRejects(t *testing.T) {
	cases := map[string]func(*Manifest){
		"bad api version": func(m *Manifest) { m.APIVersion = "v2" },
		"bad name":        func(m *Manifest) { m.Name = "Bad Name" },
		"bad version":     func(m *Manifest) { m.Version = "1.0" },
		"bad kind":        func(m *Manifest) { m.Kind = "widget" },
		"non-event sub":   func(m *Manifest) { m.Events = []string{"tasks.server.x"} },
		"bad permission":  func(m *Manifest) { m.Permissions = []string{"read-everything"} },
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			m := validManifest()
			mutate(&m)
			if err := m.Validate(); err == nil {
				t.Fatalf("%s: expected validation error, got nil", name)
			}
		})
	}
}
