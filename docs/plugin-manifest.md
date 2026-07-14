# CypherPanel Plugin Manifest (`plugin.yaml`)

**Status: reserved / finalized schema (`api_version: v1`).** The plugin loader and
runtime are post-MVP (plan.md §11). This schema is locked *now*, before any
plugin ships, so the format never has to break an existing ecosystem later.
The authoritative definition is `internal/plugins/manifest.go`; this document
mirrors it for plugin authors.

Every plugin ships a `plugin.yaml` at its root:

```yaml
api_version: v1              # required; only "v1" is accepted today
name: hello-world            # required; lowercase, 3-40 chars, [a-z0-9-], starts with a letter
version: 1.0.0               # required; semver (x.y.z)
kind: plugin                 # plugin | theme | language_pack  (default: plugin)
description: Example plugin
author: Jane Doe

# UI surfaces Core renders from this manifest — plugins never edit core UI.
ui:
  sidebar:
    - label: Hello
      path: /plugins/hello-world
      icon: puzzle
  dashboard_cards:
    - id: hello-card
      title: Hello
  settings_pages:
    - label: Hello Settings
      path: /settings/hello-world

# Domain events the plugin reacts to. Must be events.* subjects
# (see internal/events). Declares intent; the runtime enforces it.
events:
  - events.account.created
  - events.account.suspended

# Capabilities the plugin may exercise, as resource:action pairs. Enforced
# against this list at runtime — a plugin gets exactly what it declares,
# nothing ambient.
permissions:
  - accounts:read
  - servers:read
```

## Rules

- `api_version` must equal the current schema version (`v1`). New versions will
  be additive and backward-compatible; a breaking redefinition of an existing
  field is never allowed.
- Themes and language packs use the **same** manifest with `kind: theme` /
  `kind: language_pack` — they are plugin types, not special cases, layered on
  the white-labeling / i18n scaffolding.
- Backend plugins run process-isolated; `permissions` is the allowlist the
  runtime enforces. `events` subscriptions must name real `events.*` subjects.

## Validating a manifest today

Before the loader exists, `GET /api/v1/admin/plugins/manifest-schema` returns
the accepted `api_version` and `kinds`, and `internal/plugins.Manifest.Validate`
is the single gate every manifest passes.
