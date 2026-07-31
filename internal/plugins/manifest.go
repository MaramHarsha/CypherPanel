// Package plugins reserves the CypherPanel plugin surface (plan.md §11). This
// is the FINALIZED manifest schema — deliberately locked before any plugin
// ships, because a manifest format that changes afterward breaks the whole
// ecosystem. There is intentionally NO loader/runtime here yet; that is
// post-MVP. Only the schema, parsing, and validation live here now.
package plugins

import (
	"bytes"
	"fmt"
	"regexp"

	"gopkg.in/yaml.v3"
)

// MaxManifestBytes bounds a submitted plugin.yaml. A manifest is metadata, not
// content; anything larger is a mistake or an attempt to exhaust memory.
const MaxManifestBytes = 64 * 1024

// Parse decodes and validates a plugin.yaml. It is the only way a Manifest is
// produced — callers never hand-build one, so every manifest in the system has
// passed Validate.
func Parse(data []byte) (*Manifest, error) {
	if len(data) == 0 {
		return nil, fmt.Errorf("plugins: manifest is empty")
	}
	if len(data) > MaxManifestBytes {
		return nil, fmt.Errorf("plugins: manifest exceeds %d bytes", MaxManifestBytes)
	}
	var m Manifest
	// KnownFields rejects unrecognised keys: a typo'd surface or permission
	// would otherwise be silently ignored and the plugin would fail at runtime
	// with no explanation.
	dec := yaml.NewDecoder(bytes.NewReader(data))
	dec.KnownFields(true)
	if err := dec.Decode(&m); err != nil {
		return nil, fmt.Errorf("plugins: parsing manifest: %w", err)
	}
	if err := m.Validate(); err != nil {
		return nil, err
	}
	return &m, nil
}

// ManifestAPIVersion is the only manifest schema version currently accepted.
// Bump (and support the old value) only via an additive, backward-compatible
// change — never a breaking redefinition of an existing field.
const ManifestAPIVersion = "v1"

// Kind enumerates what a manifest may describe. Themes and language packs are
// plugin *types* on the same mechanism, not special cases.
const (
	KindPlugin       = "plugin"
	KindTheme        = "theme"
	KindLanguagePack = "language_pack"
)

// Manifest is the parsed, validated form of a plugin.yaml. Consumers never
// hand-build these — they come from Parse.
type Manifest struct {
	APIVersion  string   `yaml:"api_version" json:"api_version"`
	Name        string   `yaml:"name" json:"name"`
	Version     string   `yaml:"version" json:"version"`
	Kind        string   `yaml:"kind" json:"kind"`
	Description string   `yaml:"description" json:"description"`
	Author      string   `yaml:"author" json:"author"`

	// UI surfaces the plugin registers. Core renders these from the manifest;
	// plugins never edit core UI directly.
	UI UISurfaces `yaml:"ui" json:"ui"`

	// Events the plugin subscribes to (must be `events.*` subjects; see the
	// events package). Declares intent; the runtime will enforce it.
	Events []string `yaml:"events" json:"events"`

	// Permissions the plugin may exercise, enforced against this list at
	// runtime — a plugin gets exactly what it declares, nothing ambient.
	Permissions []string `yaml:"permissions" json:"permissions"`
}

type UISurfaces struct {
	Sidebar        []SidebarEntry  `yaml:"sidebar" json:"sidebar"`
	DashboardCards []DashboardCard `yaml:"dashboard_cards" json:"dashboard_cards"`
	SettingsPages  []SettingsPage  `yaml:"settings_pages" json:"settings_pages"`
}

type SidebarEntry struct {
	Label string `yaml:"label" json:"label"`
	Path  string `yaml:"path" json:"path"`
	Icon  string `yaml:"icon" json:"icon"`
}

type DashboardCard struct {
	ID    string `yaml:"id" json:"id"`
	Title string `yaml:"title" json:"title"`
}

type SettingsPage struct {
	Label string `yaml:"label" json:"label"`
	Path  string `yaml:"path" json:"path"`
}

var (
	nameRe    = regexp.MustCompile(`^[a-z][a-z0-9-]{2,39}$`)
	semverRe  = regexp.MustCompile(`^\d+\.\d+\.\d+`)
	eventRe   = regexp.MustCompile(`^events\.[a-z0-9_.]+$`)
	permRe    = regexp.MustCompile(`^[a-z_]+:[a-z_]+$`) // e.g. accounts:read
	validKind = map[string]bool{KindPlugin: true, KindTheme: true, KindLanguagePack: true}
)

// Validate checks a parsed manifest against the schema rules. It is the single
// gate every plugin passes before it is ever recorded or loaded.
func (m *Manifest) Validate() error {
	if m.APIVersion != ManifestAPIVersion {
		return fmt.Errorf("plugins: unsupported api_version %q (want %q)", m.APIVersion, ManifestAPIVersion)
	}
	if !nameRe.MatchString(m.Name) {
		return fmt.Errorf("plugins: invalid name %q (lowercase, 3-40 chars, letters/digits/-)", m.Name)
	}
	if !semverRe.MatchString(m.Version) {
		return fmt.Errorf("plugins: version %q must be semver (x.y.z)", m.Version)
	}
	if m.Kind == "" {
		m.Kind = KindPlugin
	}
	if !validKind[m.Kind] {
		return fmt.Errorf("plugins: invalid kind %q", m.Kind)
	}
	for _, ev := range m.Events {
		if !eventRe.MatchString(ev) {
			return fmt.Errorf("plugins: invalid event subscription %q (must be an events.* subject)", ev)
		}
	}
	for _, p := range m.Permissions {
		if !permRe.MatchString(p) {
			return fmt.Errorf("plugins: invalid permission %q (want resource:action)", p)
		}
	}
	return nil
}
