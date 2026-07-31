package plugins

import (
	"strings"
	"testing"
)

const goodYAML = `
api_version: v1
name: billing-sync
version: 1.2.0
kind: plugin
description: Syncs accounts to the billing system
author: Example Ltd
ui:
  sidebar:
    - label: Billing
      path: /billing
      icon: credit-card
  dashboard_cards:
    - id: mrr
      title: Monthly revenue
  settings_pages:
    - label: Billing settings
      path: /settings/billing
events:
  - events.account.created
  - events.account.suspended
permissions:
  - accounts:read
  - packages:read
`

func TestParseAcceptsAValidManifest(t *testing.T) {
	m, err := Parse([]byte(goodYAML))
	if err != nil {
		t.Fatalf("valid manifest rejected: %v", err)
	}
	if m.Name != "billing-sync" || m.Version != "1.2.0" {
		t.Errorf("identity not parsed: %+v", m)
	}
	if len(m.UI.Sidebar) != 1 || m.UI.Sidebar[0].Path != "/billing" {
		t.Errorf("sidebar surface not parsed: %+v", m.UI.Sidebar)
	}
	if len(m.Events) != 2 || len(m.Permissions) != 2 {
		t.Errorf("events/permissions not parsed: %+v", m)
	}
}

func TestParseRejectsEmptyAndOversized(t *testing.T) {
	if _, err := Parse(nil); err == nil {
		t.Error("expected an error for an empty manifest")
	}
	if _, err := Parse([]byte(strings.Repeat("x", MaxManifestBytes+1))); err == nil {
		t.Error("expected an error for an oversized manifest")
	}
}

// An unrecognised key is a typo'd surface or permission. Silently ignoring it
// would leave the plugin broken at runtime with nothing to point at.
func TestParseRejectsUnknownFields(t *testing.T) {
	_, err := Parse([]byte("api_version: v1\nname: thing-one\nversion: 1.0.0\npermisions:\n  - accounts:read\n"))
	if err == nil {
		t.Fatal("expected an error for an unknown field")
	}
}

func TestParseRunsValidation(t *testing.T) {
	// Parses as YAML, but the name violates the schema.
	_, err := Parse([]byte("api_version: v1\nname: Bad Name\nversion: 1.0.0\n"))
	if err == nil {
		t.Fatal("expected the schema validation error to surface from Parse")
	}
	if !strings.Contains(err.Error(), "invalid name") {
		t.Errorf("error should name the offending field, got: %v", err)
	}
}

func TestParseRejectsMalformedYAML(t *testing.T) {
	if _, err := Parse([]byte("api_version: v1\n  name: broken\n\t- indent")); err == nil {
		t.Error("expected an error for malformed YAML")
	}
}
